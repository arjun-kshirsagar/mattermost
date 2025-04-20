# Mattermost High Availability Implementation
## SST Project Submission - Scale

---

## Table of Contents

1. [Project Overview](#project-overview)
2. [System Architecture](#system-architecture)
3. [My Solution Walkthrough](#my-solution-walkthrough)
4. [Key Code Changes](#key-code-changes)
5. [Challenges and Solutions](#challenges-and-solutions)
6. [AWS Deployment Guide](#aws-deployment-guide)
7. [Testing and Validation](#testing-and-validation)
8. [Future Enhancements](#future-enhancements)
9. [Contributors](#contributors)
10. [License](#license)

---

## Project Overview

### Problem Statement

The open-source Mattermost Teams Edition lacks built-in support for high availability (HA) and horizontal scalability. This leads to:
- Single point of failure
- Inability to scale horizontally
- Inconsistent user experience and lack of fault tolerance

### Solution Implemented

We enabled Redis-based clustering to allow:
- Multi-server deployment
- Real-time user presence and message synchronization
- Stateless Mattermost nodes
- Fault-tolerant and horizontally scalable architecture

---

## System Architecture

### Components

1. **Mattermost Nodes**
   - Independently running application servers
   - Each handles its own WebSocket traffic
   - Stateless for easier scaling

2. **Redis Cluster**
   - Used for message propagation via Pub/Sub
   - Enables real-time event syncing
   - Manages leader election and node tracking

3. **Database Layer**
   - Shared backend for persistent storage
   - Accessed by all nodes consistently

### Architecture with Load Balancer

```ascii
                          Users
                            ↓
                    [Route 53 DNS]
                            ↓
                +----------------------+
                |   Application Load   |
                |    Balancer (ALB)    |
                +----------------------+
                    ↙            ↘
        +-----------+          +-----------+
        | Mattermost|          | Mattermost|
        | Node 1    |          | Node 2    |
        +-----------+          +-----------+
                ↘              ↙
            +---------------+
            |     Redis     |
            |    Cluster    |
            +---------------+
                    ↓
            +---------------+
            |   Database    |
            +---------------+
```

---

## My Solution Walkthrough

### Initial Setup and Codebase Exploration
- Forked the Mattermost repo
- Set up a single-node server following official setup instructions
- Faced difficulty understanding the large codebase

**Resolution:**
- Switched to reverse API tracing and backtracking to find entry points
- Identified and studied critical files (`server.go`, `service.go`, `config.json`, API routes)

### Early Attempts and Discoveries
- Attempted direct clustering via config; failed due to license restrictions
- Explored DB-loaded configs and enterprise feature restrictions

### Innovative Workarounds
- Moved config store to Google Drive via API
- Bypassed license checks
- Identified `ClusterInterface` crucial for Redis integration

### Redis Cluster Integration
- Developed dummy Redis cluster in Go
- Integrated Redis into platform code
- Implemented bash scripts for multi-node deployment
- Achieved real-time synchronization with `cluster_handlers.go`

---

## Key Code Changes

### `redisCluster` Structure

```go
type redisCluster struct {
    ps             *PlatformService
    nodeID         string
    rdb            *redis.Client
    pubsub         *redis.PubSub
    handlers       map[model.ClusterEvent][]einterfaces.ClusterMessageHandler
    handlersMutex  sync.RWMutex
    isLeader       bool
    stopChan       chan struct{}
    wg             sync.WaitGroup
}
```

### WebSocket Broadcasting

```go
func (rc *redisCluster) BroadcastWebSocketEvent(event *model.WebSocketEvent) {
    // Sends real-time event to all nodes
}
```

### Cluster Messaging Handler

```go
func (rc *redisCluster) handleClusterMessage(payload string) {
    // Deserializes and routes event to correct handler
}
```

### Leader Election Example

```go
func (rc *redisCluster) tryBecomeLeader() {
    ctx := context.Background()
    success, err := rc.rdb.SetNX(ctx, redisLeaderKey, rc.nodeID, leaderTTL).Result()
    // Leadership logic...
}
```

---

## Challenges and Solutions

| **Challenge**                          | **Resolution**                                                  |
|----------------------------------------|------------------------------------------------------------------|
| Navigating unfamiliar Go codebase      | Used backtracking, documentation, and AI explanations            |
| Understanding clustering limitations   | Bypassed license system checks                                   |
| Message sync failure                   | Adopted official handler implementations                         |
| Maintaining user presence              | Utilized cluster events for consistency                          |
| Race conditions and concurrency        | Applied mutex locks and atomic Redis ops                         |

---

## AWS Deployment Guide

### Prerequisites
- AWS account (EC2, ElastiCache, RDS)
- AWS CLI and Terraform setup

### Infrastructure Setup Snippets

**VPC Setup (Terraform)**

```hcl
resource "aws_vpc" "mattermost" {
  cidr_block = "10.0.0.0/16"
}
```

**EC2 Instances**

```hcl
resource "aws_instance" "mattermost_node" {
  ami           = "ami-xxxxx"
  instance_type = "t3.medium"
}
```

**Redis Cluster via ElastiCache**

```hcl
resource "aws_elasticache_cluster" "redis" {
  cluster_id     = "mattermost-redis"
  engine         = "redis"
  node_type      = "cache.t3.medium"
}
```

**Configuration (`config.json`)**

```json
{
  "ClusterSettings": {
    "Enable": true,
    "ClusterName": "mattermost-cluster"
  },
  "CacheSettings": {
    "CacheType": "redis",
    "RedisAddress": "YOUR_REDIS_ENDPOINT:6379"
  }
}
```

---

## Testing and Validation

### Functional Testing
- Verified presence synchronization
- Confirmed real-time message delivery

### Load Testing
- Monitored performance under user load

### Monitoring

```bash
# Redis traffic
redis-cli monitor

# Server health check
curl http://localhost:8065/api/v4/system/ping
```

---

## License

This project follows Mattermost Teams Edition licensing. All enhancements provided are educational and open.

