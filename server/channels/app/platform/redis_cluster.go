package platform

import (
    "bytes"
    "context"
    "fmt"
    "time"

    "github.com/mattermost/mattermost/server/public/model"
    "github.com/mattermost/mattermost/server/public/shared/mlog"
    "github.com/redis/go-redis/v9"
)

type RedisClusterService struct {
    client       *redis.ClusterClient
    ps           *PlatformService
    stopChan     chan struct{}
    listenerChan chan struct{}
    logger       *mlog.Logger
    isActive     bool
}

// NewRedisClusterService initializes a RedisClusterService using Redis Cluster.
func NewRedisClusterService(ps *PlatformService) (*RedisClusterService, error) {
    cfg := ps.Config().RedisSettings
    if cfg.Enable == nil || !*cfg.Enable {
        return nil, nil
    }

    logger := ps.Logger().With(mlog.String("service", "redis_cluster"))

    // Split addresses for cluster nodes
    addrs := []string{}
    if cfg.ClusterAddresses != nil && len(cfg.ClusterAddresses) > 0 {
        addrs = cfg.ClusterAddresses
    } else if cfg.Address != nil {
        addrs = []string{*cfg.Address}
    } else {
        return nil, fmt.Errorf("no Redis addresses provided")
    }

    options := &redis.ClusterOptions{
        Addrs:    addrs,
        Password: "",
    }

    if cfg.Password != nil {
        options.Password = *cfg.Password
    }

    if cfg.PoolSize != nil {
        options.PoolSize = *cfg.PoolSize
    }

    if cfg.PoolTimeoutSeconds != nil && *cfg.PoolTimeoutSeconds > 0 {
        options.PoolTimeout = time.Duration(*cfg.PoolTimeoutSeconds) * time.Second
    }

    if cfg.ConnectTimeoutMs != nil && *cfg.ConnectTimeoutMs > 0 {
        options.DialTimeout = time.Duration(*cfg.ConnectTimeoutMs) * time.Millisecond
    }

    if cfg.ReadTimeoutMs != nil && *cfg.ReadTimeoutMs > 0 {
        options.ReadTimeout = time.Duration(*cfg.ReadTimeoutMs) * time.Millisecond
    }

    if cfg.WriteTimeoutMs != nil && *cfg.WriteTimeoutMs > 0 {
        options.WriteTimeout = time.Duration(*cfg.WriteTimeoutMs) * time.Millisecond
    }

    client := redis.NewClusterClient(options)

    // Test the connection
    ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
    defer cancel()
    if err := client.Ping(ctx).Err(); err != nil {
        return nil, fmt.Errorf("failed to connect to Redis Cluster: %w", err)
    }

    logger.Info("Connected to Redis Cluster", mlog.Strings("addresses", addrs))

    return &RedisClusterService{
        client:       client,
        ps:           ps,
        stopChan:     make(chan struct{}),
        listenerChan: make(chan struct{}),
        logger:       logger,
    }, nil
}

// Start begins listening for Pub/Sub events from Redis Cluster.
func (rcs *RedisClusterService) Start() {
    if rcs == nil || rcs.isActive {
        return
    }

    go func() {
        defer func() {
            close(rcs.listenerChan)
        }()

        ctx, cancel := context.WithCancel(context.Background())
        defer cancel()

        pubsub := rcs.client.PSubscribe(ctx, "mattermost_*")
        defer pubsub.Close()

        rcs.isActive = true
        rcs.logger.Info("Redis cluster service started")

        for {
            select {
            case <-rcs.stopChan:
                rcs.logger.Info("Redis cluster service stopped")
                return
            case msg, ok := <-pubsub.Channel():
                if !ok {
                    rcs.logger.Error("Redis pubsub channel closed unexpectedly")
                    return
                }

                event, err := deserializeEvent(msg.Payload)
                if err != nil {
                    rcs.logger.Error("Failed to deserialize event", mlog.Err(err))
                    continue
                }

                // Deliver the event to the websocket hub
                rcs.ps.PublishWebSocketEvent(string(event.EventType()), event.GetBroadcast().UserId, event.GetData(), event.GetBroadcast())
            }
        }
    }()
}

// Stop shuts down the RedisClusterService and cleans up resources.
func (rcs *RedisClusterService) Stop() {
    if rcs == nil || !rcs.isActive {
        return
    }

    close(rcs.stopChan)
    <-rcs.listenerChan
    if err := rcs.client.Close(); err != nil {
        rcs.logger.Error("Error closing Redis cluster client", mlog.Err(err))
    }
    rcs.isActive = false
}

// PublishEvent publishes a WebSocket event to the Redis Cluster.
func (rcs *RedisClusterService) PublishEvent(event *model.WebSocketEvent) {
    if rcs == nil || !rcs.isActive {
        return
    }

    channelName := fmt.Sprintf("mattermost_%s", event.GetBroadcast().ChannelId)
    payload, err := serializeEvent(event)
    if err != nil {
        rcs.logger.Error("Failed to serialize event", mlog.Err(err))
        return
    }

    ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
    defer cancel()

    if err := rcs.client.Publish(ctx, channelName, payload).Err(); err != nil {
        rcs.logger.Error("Failed to publish event to Redis", mlog.Err(err))
    }
}

// serializeEvent serializes a WebSocketEvent to JSON string.
func serializeEvent(event *model.WebSocketEvent) (string, error) {
    data, err := event.ToJSON()
    if err != nil {
        return "", err
    }
    return string(data), nil
}

// deserializeEvent deserializes a JSON string to a WebSocketEvent.
func deserializeEvent(data string) (*model.WebSocketEvent, error) {
    return model.WebSocketEventFromJSON(bytes.NewReader([]byte(data)))
}