// server/channels/app/platform/redis_cluster.go

package platform

import (
    "context"
    "encoding/json"
    "fmt"
    "sync"
    "time"
    "sync/atomic"

    "github.com/redis/go-redis/v9"
    
    "github.com/mattermost/mattermost/server/public/model"
    "github.com/mattermost/mattermost/server/public/shared/mlog"
    "github.com/mattermost/mattermost/server/public/shared/request"
    "github.com/mattermost/mattermost/server/v8/einterfaces"
)

const (
    redisPubSubChannel   = "mattermost_cluster"
    redisLeaderKey      = "mattermost_cluster_leader"
    redisNodesKey       = "mattermost_cluster_nodes"
    redisEventChannel   = "mattermost_cluster_events"
    leaderTTL          = 15 * time.Second
    nodesTTL           = 20 * time.Second
    heartbeatInterval  = 5 * time.Second
)

type redisCluster struct {
    ps             *PlatformService
    nodeID         string
    rdb            *redis.Client
    pubsub         *redis.PubSub
    handlers       map[model.ClusterEvent][]einterfaces.ClusterMessageHandler
    handlersMutex  sync.RWMutex
    isLeader       bool
    leaderMutex    sync.RWMutex
    stopChan       chan struct{}
    wg             sync.WaitGroup
    logger         *mlog.Logger
    isReady        atomic.Bool
    messageBuffer  []model.ClusterMessage  // Optional: buffer messages
    broadcastHooks map[string]BroadcastHook
}

func NewRedisCluster(ps *PlatformService, hooks map[string]BroadcastHook) *redisCluster {
    nodeID := model.NewId()
    rc := &redisCluster{
        ps:             ps,
        nodeID:         nodeID,
        handlers:       make(map[model.ClusterEvent][]einterfaces.ClusterMessageHandler),
        stopChan:       make(chan struct{}),
        logger:         ps.logger.With(mlog.String("cluster_node_id", nodeID)),
        broadcastHooks: hooks,
    }

    cfg := ps.Config().CacheSettings
    rc.rdb = redis.NewClient(&redis.Options{
        Addr:     *cfg.RedisAddress,
        Password: *cfg.RedisPassword,
        DB:       int(*cfg.RedisDB),
    })

    // Register the platform's cluster handlers
    rc.RegisterClusterMessageHandler(model.ClusterEventPublish, ps.ClusterPublishHandler)
    rc.RegisterClusterMessageHandler(model.ClusterEventUpdateStatus, ps.ClusterUpdateStatusHandler)
    rc.RegisterClusterMessageHandler(model.ClusterEventInvalidateAllCaches, ps.ClusterInvalidateAllCachesHandler)
    rc.RegisterClusterMessageHandler(model.ClusterEventInvalidateWebConnCacheForUser, ps.clusterInvalidateWebConnSessionCacheForUserHandler)
    rc.RegisterClusterMessageHandler(model.ClusterEventBusyStateChanged, ps.clusterBusyStateChgHandler)

    return rc
}

func (rc *redisCluster) Start() error {
    ctx := context.Background()
    
    // Test Redis connection
    if err := rc.rdb.Ping(ctx).Err(); err != nil {
        return fmt.Errorf("failed to connect to Redis: %w", err)
    }

    rc.logger.Info("Redis cluster connection established",
        mlog.String("node_id", rc.nodeID),
        mlog.String("redis_addr", rc.rdb.Options().Addr))

    // Subscribe to cluster events
    rc.pubsub = rc.rdb.Subscribe(ctx, redisPubSubChannel, redisEventChannel)
    
    // Start cluster routines
    rc.wg.Add(3)
    go rc.leaderElectionLoop()
    go rc.heartbeatLoop()
    go rc.messageListener()

    return nil
}

func (rc *redisCluster) Stop() {
    close(rc.stopChan)
    if rc.pubsub != nil {
        rc.pubsub.Close()
    }
    rc.wg.Wait()
    rc.logger.Info("Redis cluster stopped", mlog.String("node_id", rc.nodeID))
}

func (rc *redisCluster) leaderElectionLoop() {
    defer rc.wg.Done()
    ticker := time.NewTicker(leaderTTL / 2)
    defer ticker.Stop()

    for {
        select {
        case <-rc.stopChan:
            return
        case <-ticker.C:
            rc.tryBecomeLeader()
        }
    }
}

func (rc *redisCluster) tryBecomeLeader() {
    ctx := context.Background()
    wasLeader := rc.IsLeader()

    success, err := rc.rdb.SetNX(ctx, redisLeaderKey, rc.nodeID, leaderTTL).Result()
    if err != nil {
        rc.logger.Error("Failed to perform leader election", mlog.Err(err))
        return
    }

    rc.leaderMutex.Lock()
    rc.isLeader = success
    rc.leaderMutex.Unlock()

    if wasLeader != success {
        if success {
            rc.logger.Info("Became cluster leader")
        } else {
            rc.logger.Info("Lost cluster leadership")
        }
        rc.ps.InvokeClusterLeaderChangedListeners()
    }

    if success {
        if err := rc.rdb.Expire(ctx, redisLeaderKey, leaderTTL).Err(); err != nil {
            rc.logger.Error("Failed to refresh leader TTL", mlog.Err(err))
        }
    }
}

func (rc *redisCluster) heartbeatLoop() {
    defer rc.wg.Done()
    ticker := time.NewTicker(heartbeatInterval)
    defer ticker.Stop()

    for {
        select {
        case <-rc.stopChan:
            return
        case <-ticker.C:
            rc.sendHeartbeat()
        }
    }
}

func (rc *redisCluster) sendHeartbeat() {
    ctx := context.Background()
    nodeInfo := &model.ClusterInfo{
        Id:      rc.nodeID,
        Version: model.CurrentVersion,
    }
    
    data, err := json.Marshal(nodeInfo)
    if err != nil {
        rc.logger.Error("Failed to marshal node info", mlog.Err(err))
        return
    }

    key := fmt.Sprintf("%s:%s", redisNodesKey, rc.nodeID)
    if err := rc.rdb.Set(ctx, key, string(data), nodesTTL).Err(); err != nil {
        rc.logger.Error("Failed to send heartbeat", mlog.Err(err))
    }
}

func (rc *redisCluster) messageListener() {
    defer rc.wg.Done()
    ch := rc.pubsub.Channel()

    for {
        select {
        case <-rc.stopChan:
            return
        case msg := <-ch:
            if msg == nil {
                continue
            }
            
            switch msg.Channel {
            case redisPubSubChannel:
                rc.handleClusterMessage(msg.Payload)
            case redisEventChannel:
                rc.handleEventMessage(msg.Payload)
            }
        }
    }
}

func (rc *redisCluster) handleClusterMessage(payload string) {
    var msg model.ClusterMessage
    if err := json.Unmarshal([]byte(payload), &msg); err != nil {
        rc.logger.Error("Failed to unmarshal cluster message", mlog.Err(err))
        return
    }

    rc.logger.Debug("Received cluster message",
        mlog.String("event", string(msg.Event)),
        mlog.String("node_id", rc.nodeID))

    // Get handlers for this event
    rc.handlersMutex.RLock()
    handlers, ok := rc.handlers[msg.Event]
    rc.handlersMutex.RUnlock()

    if !ok {
        rc.logger.Debug("No handlers for cluster event",
            mlog.String("event", string(msg.Event)))
        return
    }

    // Execute all handlers for this event
    for _, handler := range handlers {
        handler(&msg)
    }
}

func (rc *redisCluster) handleEventMessage(payload string) {
    var ev model.PluginClusterEvent
    if err := json.Unmarshal([]byte(payload), &ev); err != nil {
        rc.logger.Error("Failed to unmarshal plugin event", mlog.Err(err))
        return
    }

    rc.handlersMutex.RLock()
    handlers, ok := rc.handlers[model.ClusterEventPluginEvent]
    rc.handlersMutex.RUnlock()

    if !ok {
        return
    }

    msg := &model.ClusterMessage{
        Event: model.ClusterEventPluginEvent,
        Props: map[string]string{
            "EventId": ev.Id,
        },
        Data: ev.Data,
    }

    for _, handler := range handlers {
        handler(msg)
    }
}

func (rc *redisCluster) hasHandler(event model.ClusterEvent) bool {
    rc.handlersMutex.RLock()
    defer rc.handlersMutex.RUnlock()
    _, ok := rc.handlers[event]
    return ok
}

// ClusterInterface implementation
func (rc *redisCluster) StartInterNodeCommunication() {
    if err := rc.Start(); err != nil {
        rc.logger.Error("Failed to start cluster communication", mlog.Err(err))
    }
}

func (rc *redisCluster) StopInterNodeCommunication() {
    rc.Stop()
}

func (rc *redisCluster) RegisterClusterMessageHandler(event model.ClusterEvent, handler einterfaces.ClusterMessageHandler) {
    rc.handlersMutex.Lock()
    defer rc.handlersMutex.Unlock()
    rc.handlers[event] = append(rc.handlers[event], handler)
}

func (rc *redisCluster) GetClusterId() string {
    return rc.nodeID
}

func (rc *redisCluster) IsLeader() bool {
    rc.leaderMutex.RLock()
    defer rc.leaderMutex.RUnlock()
    return rc.isLeader
}

func (rc *redisCluster) GetMyClusterInfo() *model.ClusterInfo {
    return &model.ClusterInfo{
        Id:      rc.nodeID,
        Version: model.CurrentVersion,
    }
}

func (rc *redisCluster) GetClusterInfos() []*model.ClusterInfo {
    ctx := context.Background()
    pattern := fmt.Sprintf("%s:*", redisNodesKey)
    keys, err := rc.rdb.Keys(ctx, pattern).Result()
    if err != nil {
        rc.logger.Error("Failed to get cluster nodes", mlog.Err(err))
        return nil
    }

    var infos []*model.ClusterInfo
    for _, key := range keys {
        data, err := rc.rdb.Get(ctx, key).Result()
        if err != nil {
            continue
        }

        var info model.ClusterInfo
        if err := json.Unmarshal([]byte(data), &info); err != nil {
            continue
        }
        infos = append(infos, &info)
    }
    return infos
}

func (rc *redisCluster) SendClusterMessage(msg *model.ClusterMessage) {
    data, err := json.Marshal(msg)
    if err != nil {
        rc.logger.Error("Failed to marshal cluster message", mlog.Err(err))
        return
    }

    channel := redisPubSubChannel
    if msg.Event == model.ClusterEventPluginEvent {
        channel = redisEventChannel
    }

    ctx := context.Background()
    if msg.SendType == model.ClusterSendReliable {
        // For reliable sending, use Redis RPUSH to a list
        key := fmt.Sprintf("%s:reliable:%s", redisPubSubChannel, msg.Event)
        if err := rc.rdb.RPush(ctx, key, string(data)).Err(); err != nil {
            rc.logger.Error("Failed to push reliable cluster message", mlog.Err(err))
            return
        }
        // Set TTL on the list
        rc.rdb.Expire(ctx, key, 24*time.Hour)
    }

    // Always publish to ensure immediate delivery
    if err := rc.rdb.Publish(ctx, channel, string(data)).Err(); err != nil {
        rc.logger.Error("Failed to publish cluster message", mlog.Err(err))
    }
}

func (rc *redisCluster) SendClusterMessageToNode(nodeID string, msg *model.ClusterMessage) error {
    // In Redis implementation, all messages are broadcasted
    rc.SendClusterMessage(msg)
    return nil
}

func (rc *redisCluster) GetClusterStats(ctx request.CTX) ([]*model.ClusterStats, *model.AppError) {
    nodes := rc.GetClusterInfos()
    stats := make([]*model.ClusterStats, 0, len(nodes))
    
    for _, node := range nodes {
        stat := &model.ClusterStats{
            Id:            node.Id,
        }
        stats = append(stats, stat)
    }
    
    return stats, nil
}

func (rc *redisCluster) GetLogs(ctx request.CTX, page, perPage int) ([]string, *model.AppError) {
    // Not implemented for Redis cluster
    return []string{}, nil
}

func (rc *redisCluster) GetPluginStatuses() (model.PluginStatuses, *model.AppError) {
    // Not implemented for Redis cluster
    return model.PluginStatuses{}, nil
}

func (rc *redisCluster) ConfigChanged(old, new *model.Config, sendToOtherServer bool) *model.AppError {
    // Handle config changes if needed
    return nil
}

func (rc *redisCluster) GenerateSupportPacket(ctx request.CTX, opts *model.SupportPacketOptions) (map[string][]model.FileData, error) {
    result := make(map[string][]model.FileData)
    
    // Add cluster information
    clusterInfo := &model.ClusterInfo{
        Id:      rc.nodeID,
        Version: model.CurrentVersion,
    }
    
    infoBytes, err := json.Marshal(clusterInfo)
    if err != nil {
        return nil, fmt.Errorf("failed to marshal cluster info: %w", err)
    }
    
    result["cluster_info"] = []model.FileData{{
        Filename: "cluster_info.json",
        Body:     infoBytes,
    }}
    
    // Add node status
    nodes := rc.GetClusterInfos()
    nodesBytes, err := json.Marshal(nodes)
    if err != nil {
        return nil, fmt.Errorf("failed to marshal nodes info: %w", err)
    }
    
    result["cluster_nodes"] = []model.FileData{{
        Filename: "cluster_nodes.json",
        Body:     nodesBytes,
    }}
    
    return result, nil
}

func (rc *redisCluster) GetWSQueues(userID, connectionID string, seqNum int64) (map[string]*model.WSQueues, error) {
    // For Redis implementation, we don't track WebSocket queues per node
    // Return empty map as we handle WebSocket events through Redis pub/sub
    return map[string]*model.WSQueues{}, nil
}

func (rc *redisCluster) QueryLogs(rctx request.CTX, page, perPage int) (map[string][]string, *model.AppError) {
    // Not implemented for Redis cluster
    return map[string][]string{}, nil
}

func (rc *redisCluster) WebConnCountForUser(userID string) (int, *model.AppError) {
    // For Redis implementation, we don't track connection counts per node
    return 0, nil
}

func (rc *redisCluster) NotifyMsg(buf []byte) {
    // For Redis implementation, we use pub/sub instead
}

func (rc *redisCluster) HealthScore() int {
    // Return 0 for "totally healthy"
    return 0
}

func (rc *redisCluster) SetReady() {
    rc.isReady.Store(true)
}

// Add method to handle direct WebSocket broadcasts
func (rc *redisCluster) BroadcastWebSocketEvent(event *model.WebSocketEvent) {
    // Skip if the event is already from another cluster node
    if event.IsFromCluster() {
        return
    }

    // Mark the event as coming from cluster
    event.SetFromCluster(true)

    // Convert WebSocketEvent to JSON bytes
    data, err := event.ToJSON()
    if err != nil {
        rc.logger.Error("Failed to marshal WebSocket event", mlog.Err(err))
        return
    }

    // Create cluster message
    msg := &model.ClusterMessage{
        Event:    model.ClusterEventPublish,
        Data:     data,
        SendType: model.ClusterSendBestEffort,
    }

    // Use reliable sending for certain event types
    if event.EventType() == model.WebsocketEventPosted ||
        event.EventType() == model.WebsocketEventPostEdited ||
        event.EventType() == model.WebsocketEventDirectAdded ||
        event.EventType() == model.WebsocketEventGroupAdded ||
        event.EventType() == model.WebsocketEventAddedToTeam ||
        event.GetBroadcast().ReliableClusterSend {
        msg.SendType = model.ClusterSendReliable
    }

    // Send to other nodes via Redis
    rc.SendClusterMessage(msg)
}

// Update the WebSocket event broadcasting
func (rc *redisCluster) Publish(event *model.WebSocketEvent) {
    // Skip if the event is already from another cluster node
    if event.IsFromCluster() {
        return
    }

    // Mark the event as coming from cluster
    event.SetFromCluster(true)

    // Set the node ID in the broadcast
    if event.GetBroadcast() != nil {
        event.GetBroadcast().ConnectionId = rc.nodeID
    }

    // Local broadcast first
    rc.ps.PublishSkipClusterSend(event)

    // Prepare cluster message
    data, err := event.ToJSON()  // Handle both return values
    if err != nil {
        rc.logger.Error("Failed to marshal WebSocket event", mlog.Err(err))
        return
    }

    msg := &model.ClusterMessage{
        Event: model.ClusterEventPublish,
        Data:  data,
    }

    // Send to other nodes via Redis
    rc.SendClusterMessage(msg)
}

func (rc *redisCluster) processReliableMessages() {
    defer rc.wg.Done()
    ticker := time.NewTicker(1 * time.Second)
    defer ticker.Stop()

    for {
        select {
        case <-rc.stopChan:
            return
        case <-ticker.C:
            ctx := context.Background()
            // Process reliable messages
            pattern := fmt.Sprintf("%s:reliable:*", redisPubSubChannel)
            keys, err := rc.rdb.Keys(ctx, pattern).Result()
            if err != nil {
                rc.logger.Error("Failed to get reliable message keys", mlog.Err(err))
                continue
            }

            for _, key := range keys {
                // Get all messages in the list
                messages, err := rc.rdb.LRange(ctx, key, 0, -1).Result()
                if err != nil {
                    rc.logger.Error("Failed to get reliable messages", mlog.String("key", key), mlog.Err(err))
                    continue
                }

                for _, msg := range messages {
                    rc.handleClusterMessage(msg)
                }
            }
        }
    }
}