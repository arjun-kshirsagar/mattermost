//go:build oss_cluster
// +build oss_cluster

package platform

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/mattermost/mattermost/server/public/model"
	"github.com/mattermost/mattermost/server/public/shared/mlog"
	"github.com/mattermost/mattermost/server/public/shared/request"
	"github.com/mattermost/mattermost/server/v8/einterfaces"
)

/******************************************************************************
* Constants & helpers                                                         *
******************************************************************************/

const (
	redisChannel = "mm_cluster_events"

	leaderKey  = "mm_cluster_leader"
	leaderTTL  = 25 * time.Second
	leaderTick = leaderTTL / 2

	healthScoreHealthy  = 0
	healthScoreDegraded = 100
)

func makeRedis(ps *PlatformService) *redis.Client {
	cfg := ps.Config().CacheSettings
	return redis.NewClient(&redis.Options{
		Addr:     *cfg.RedisAddress,
		Password: *cfg.RedisPassword,
		DB:       int(*cfg.RedisDB),
	})
}

// idiots‑simple atomic bool ---------------------------------------------------
type atomicBool struct{ v atomic.Int32 }

func (a *atomicBool) set(x bool) { if x { a.v.Store(1) } else { a.v.Store(0) } }
func (a *atomicBool) get() bool  { return a.v.Load() == 1 }

/******************************************************************************
* redisCluster                                                                *
******************************************************************************/

type redisCluster struct {
	ps *PlatformService
	id string

	rdb    *redis.Client
	pubsub *redis.PubSub

	handlersMu sync.RWMutex
	handlers   map[model.ClusterEvent][]einterfaces.ClusterMessageHandler

	stop   chan struct{}
	wg     sync.WaitGroup
	leader atomicBool
}

/******************************************************************************
* Construction & start/stop                                                   *
******************************************************************************/

// NewRedisCluster only prepares the struct – call StartInterNodeCommunication
// once the platform & its handlers are fully initialised.
func NewRedisCluster(ps *PlatformService) *redisCluster {
	return &redisCluster{
		ps:       ps,
		id:       model.NewId(),
		rdb:      makeRedis(ps),
		handlers: map[model.ClusterEvent][]einterfaces.ClusterMessageHandler{},
		stop:     make(chan struct{}),
	}
}

// called by Mattermost core during bootstrap *after* it has attached handlers.
func (rc *redisCluster) StartInterNodeCommunication() {
	ctx := context.Background()

	// first connection test – bail out early if Redis isn’t reachable
	if err := rc.rdb.Ping(ctx).Err(); err != nil {
		mlog.Fatal("redis_cluster: cannot connect to Redis", mlog.Err(err))
	}

	// best‑effort initial leader claim
	_, _ = rc.rdb.SetNX(ctx, leaderKey, rc.id, leaderTTL).Result()

	// subscribe
	rc.pubsub = rc.rdb.Subscribe(ctx, redisChannel)
	// receive confirmation so that Channel() won’t miss the first message
	if _, err := rc.pubsub.Receive(ctx); err != nil {
		mlog.Fatal("redis_cluster: subscribe failed", mlog.Err(err))
	}

	// event loop goroutines
	rc.wg.Add(2)
	go rc.runSubscriber()
	go rc.runLeaderElection()

	mlog.Info("redis_cluster: started",
		mlog.String("node_id", rc.id),
		mlog.String("redis", rc.rdb.Options().Addr))
}

func (rc *redisCluster) StopInterNodeCommunication() {
	close(rc.stop)
	_ = rc.pubsub.Close()
	rc.wg.Wait()
	_ = rc.rdb.Close()
}

func (rc *redisCluster) runLeaderElection() {
	defer rc.wg.Done()
	ticker := time.NewTicker(leaderTick)
	defer ticker.Stop()

	ctx := context.Background()

	for {
		select {
		case <-rc.stop:
			return
		case <-ticker.C:
			ok, err := rc.rdb.SetNX(ctx, leaderKey, rc.id, leaderTTL).Result()
			if err != nil {
				mlog.Warn("redis_cluster: leader‑election error", mlog.Err(err))
				continue
			}

			currentLeader := rc.leader.get()
			if ok && !currentLeader { // became leader
				rc.leader.set(true)
				mlog.Info("redis_cluster: became leader")
				rc.ps.InvokeClusterLeaderChangedListeners()
			} else if !ok {
				holder, _ := rc.rdb.Get(ctx, leaderKey).Result()
				if holder != rc.id && currentLeader { // lost leadership
					rc.leader.set(false)
					mlog.Info("redis_cluster: lost leadership")
					rc.ps.InvokeClusterLeaderChangedListeners()
				}
			}
		}
	}
}

/******************************************************************************
* PUB/SUB                                                                     *
******************************************************************************/

type envelope struct {
	From string                `json:"from"`
	Msg  *model.ClusterMessage `json:"msg"`
}

func (rc *redisCluster) runSubscriber() {
	defer rc.wg.Done()

	for {
		select {
		case <-rc.stop:
			return
		case msg := <-rc.pubsub.Channel():
			if msg == nil {
				continue
			}
			var env envelope
			if err := json.Unmarshal([]byte(msg.Payload), &env); err != nil {
				continue
			}
			if env.From == rc.id { // self‑echo
				continue
			}

			// honour optional single‑target optimisation
			if tgt, ok := env.Msg.Props["_target"]; ok && tgt != rc.id {
				continue
			}

			rc.dispatch(env.Msg)
		}
	}
}

func (rc *redisCluster) dispatch(cm *model.ClusterMessage) {
	rc.handlersMu.RLock()
	hs := rc.handlers[cm.Event]
	rc.handlersMu.RUnlock()

	for _, h := range hs {
		hCopy := h      // capture
		msgCopy := cm   // capture
		rc.ps.Go(func() { hCopy(msgCopy) })
	}
}

/******************************************************************************
*  ClusterInterface implementation                                            *
******************************************************************************/

func (rc *redisCluster) RegisterClusterMessageHandler(ev model.ClusterEvent, h einterfaces.ClusterMessageHandler) {
	rc.handlersMu.Lock()
	rc.handlers[ev] = append(rc.handlers[ev], h)
	rc.handlersMu.Unlock()
}

func (rc *redisCluster) SendClusterMessage(cm *model.ClusterMessage) {
	_ = rc.SendClusterMessageToNode("", cm)
}

func (rc *redisCluster) SendClusterMessageToNode(nodeID string, cm *model.ClusterMessage) error {
	if nodeID != "" {
		if cm.Props == nil {
			cm.Props = map[string]string{}
		}
		cm.Props["_target"] = nodeID
	}

	b, _ := json.Marshal(envelope{From: rc.id, Msg: cm})
	return rc.rdb.Publish(context.Background(), redisChannel, b).Err()
}

/*** trivial / metadata  ******************************************************/

func (rc *redisCluster) GetClusterId() string { return rc.id }
func (rc *redisCluster) IsLeader() bool       { return rc.leader.get() }

func (rc *redisCluster) HealthScore() int {
	if err := rc.rdb.Ping(context.Background()).Err(); err != nil {
		return healthScoreDegraded
	}
	return healthScoreHealthy
}

func (rc *redisCluster) GetMyClusterInfo() *model.ClusterInfo {
	hn, _ := os.Hostname()
	return &model.ClusterInfo{
		Id:         rc.id,
		Version:    model.CurrentVersion,
		Hostname:   hn,
		IsHealthy:  rc.HealthScore() == healthScoreHealthy,
		IsLeader:   rc.leader.get(),
		ClusterId:  rc.id,
		CreateAt:   model.GetMillis(),
		LastPingAt: model.GetMillis(),
	}
}
func (rc *redisCluster) GetClusterInfos() []*model.ClusterInfo {
	return []*model.ClusterInfo{rc.GetMyClusterInfo()}
}

/*** stubs that the OSS build never calls ************************************/

func (rc *redisCluster) NotifyMsg(_ []byte)                                                    {}
func (rc *redisCluster) GetClusterStats(request.CTX) ([]*model.ClusterStats, *model.AppError)  { return nil, nil }
func (rc *redisCluster) GetLogs(request.CTX, int, int) ([]string, *model.AppError)             { return nil, nil }
func (rc *redisCluster) QueryLogs(request.CTX, int, int) (map[string][]string, *model.AppError){ return nil, nil }
func (rc *redisCluster) GenerateSupportPacket(request.CTX, *model.SupportPacketOptions)(map[string][]model.FileData,error){ return nil, errors.New("not implemented") }
func (rc *redisCluster) GetPluginStatuses()(model.PluginStatuses,*model.AppError){ return nil, nil }
func (rc *redisCluster) ConfigChanged(*model.Config,*model.Config,bool)*model.AppError          { return nil }
func (rc *redisCluster) WebConnCountForUser(string)(int,*model.AppError)                        { return 0, nil }
func (rc *redisCluster) GetWSQueues(string,string,int64)(map[string]*model.WSQueues,error)      { return nil, nil }

/******************************************************************************
* Helpers                                                                     *
******************************************************************************/

func (rc *redisCluster) runPingForHealth() { /* optional extra health tracking */ }

func ip() string { if addrs, _ := net.InterfaceAddrs(); len(addrs) > 0 { return addrs[0].String() }; return "" }
