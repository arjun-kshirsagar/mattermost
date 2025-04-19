package cluster

import (
	"encoding/json"
	"sync"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/mattermost/mattermost/server/public/model"
	"github.com/mattermost/mattermost/server/public/shared/mlog"
	"github.com/mattermost/mattermost/server/public/shared/request"
)

const (
	clusterPubSubChannel = "mattermost_cluster"
	clusterKeyPrefix    = "cluster:"
	nodeInfoTTL        = 30 * time.Second
)

type ClusterService struct {
	nodeID             string
	redis             *redis.Client
	handlers          map[model.ClusterEvent]einterfaces.ClusterMessageHandler
	handlersMutex     sync.RWMutex
	logger            *mlog.Logger
	isRunning         bool
	stopChan          chan struct{}
}

func NewClusterService(redisURL string, logger *mlog.Logger) (*ClusterService, error) {
	opt, err := redis.ParseURL(redisURL)
	if err != nil {
		return nil, err
	}

	client := redis.NewClient(opt)
	
	return &ClusterService{
		nodeID:    model.NewId(),
		redis:     client,
		handlers:  make(map[model.ClusterEvent]einterfaces.ClusterMessageHandler),
		logger:    logger,
		stopChan:  make(chan struct{}),
	}, nil
}

func (c *ClusterService) StartInterNodeCommunication() {
	if c.isRunning {
		return
	}

	c.isRunning = true

	// Start Redis Pub/Sub subscriber
	go c.subscribeToClusterEvents()
	
	// Start heartbeat to maintain node presence
	go c.startHeartbeat()
}

func (c *ClusterService) StopInterNodeCommunication() {
	if !c.isRunning {
		return
	}

	close(c.stopChan)
	c.isRunning = false
}

func (c *ClusterService) subscribeToClusterEvents() {
	pubsub := c.redis.Subscribe(context.Background(), clusterPubSubChannel)
	defer pubsub.Close()

	for {
		select {
		case <-c.stopChan:
			return
		default:
			msg, err := pubsub.ReceiveMessage(context.Background())
			if err != nil {
				c.logger.Error("Error receiving cluster message", mlog.Err(err))
				continue
			}

			var clusterMsg model.ClusterMessage
			if err := json.Unmarshal([]byte(msg.Payload), &clusterMsg); err != nil {
				c.logger.Error("Error unmarshaling cluster message", mlog.Err(err))
				continue
			}

			// Don't process messages from self
			if clusterMsg.SenderId == c.nodeID {
				continue
			}

			c.handleClusterMessage(&clusterMsg)
		}
	}
}

func (c *ClusterService) handleClusterMessage(msg *model.ClusterMessage) {
	c.handlersMutex.RLock()
	handler, ok := c.handlers[msg.Event]
	c.handlersMutex.RUnlock()

	if !ok {
		return
	}

	handler(msg)
}

func (c *ClusterService) RegisterClusterMessageHandler(event model.ClusterEvent, handler einterfaces.ClusterMessageHandler) {
	c.handlersMutex.Lock()
	c.handlers[event] = handler
	c.handlersMutex.Unlock()
}

func (c *ClusterService) SendClusterMessage(msg *model.ClusterMessage) {
	msg.SenderId = c.nodeID
	msg.CreateAt = model.GetMillis()

	data, err := json.Marshal(msg)
	if err != nil {
		c.logger.Error("Error marshaling cluster message", mlog.Err(err))
		return
	}

	if err := c.redis.Publish(context.Background(), clusterPubSubChannel, data).Err(); err != nil {
		c.logger.Error("Error publishing cluster message", mlog.Err(err))
	}
}

func (c *ClusterService) startHeartbeat() {
	ticker := time.NewTicker(nodeInfoTTL / 2)
	defer ticker.Stop()

	for {
		select {
		case <-c.stopChan:
			return
		case <-ticker.C:
			info := &model.ClusterInfo{
				Id:         c.nodeID,
				LastPing:   model.GetMillis(),
				Version:    model.CurrentVersion,
			}

			data, err := json.Marshal(info)
			if err != nil {
				c.logger.Error("Error marshaling node info", mlog.Err(err))
				continue
			}

			key := clusterKeyPrefix + c.nodeID
			if err := c.redis.Set(context.Background(), key, data, nodeInfoTTL).Err(); err != nil {
				c.logger.Error("Error setting node info", mlog.Err(err))
			}
		}
	}
}

func (c *ClusterService) IsLeader() bool {
	// Get all node keys
	keys, err := c.redis.Keys(context.Background(), clusterKeyPrefix+"*").Result()
	if err != nil {
		c.logger.Error("Error getting cluster nodes", mlog.Err(err))
		return false
	}

	// Node with lowest ID is the leader
	lowestID := c.nodeID
	for _, key := range keys {
		nodeID := key[len(clusterKeyPrefix):]
		if nodeID < lowestID {
			lowestID = nodeID
		}
	}

	return lowestID == c.nodeID
}

func (c *ClusterService) GetClusterId() string {
	return "cluster_" + c.nodeID[:8]
}
