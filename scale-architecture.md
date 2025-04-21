# Architecture Design and Documentation

## 1. Overview of the Approach

We enhance the open-source Mattermost platform by enabling **multi-server deployment**, **consistent message history**, and **real-time synchronization** across servers.  
The solution focuses on **decoupling** critical stateful components, adding **distributed coordination**, and supporting **horizontal scaling** without breaking the real-time experience.

---

## 2. System Architecture

### **High-Level Diagram**

```
            ┌────────────────────────────┐
            │         Load Balancer       │
            └────────────────────────────┘
                        │
        ┌───────────────┼────────────────┬───────────────┐
        │               │                │               │
 ┌────────────┐   ┌────────────┐   ┌────────────┐   ┌────────────┐
 │ App Server │   │ App Server │   │ App Server │   │ App Server │
 │   Node 1   │   │   Node 2    │   │   Node 3    │   │   Node 4    │
 └────────────┘   └────────────┘   └────────────┘   └────────────┘
        │               │                │               │
 ┌─────────────────────────────────────────────────────────────┐
 │                   Shared Database (Primary/Replica)         │
 └─────────────────────────────────────────────────────────────┘
        │
 ┌─────────────────────────────────────────────────────────────┐
 │                     Redis Pub/Sub Cluster                   │
 └─────────────────────────────────────────────────────────────┘
        │
 ┌─────────────────────────────────────────────────────────────┐
 │             File Storage (Shared NFS / S3-Compatible)        │
 └─────────────────────────────────────────────────────────────┘
```

---

### **Component Breakdown**

| Component | Description |
|:----------|:------------|
| **Load Balancer (HAProxy / Nginx / AWS ELB)** | Distributes user traffic across app servers evenly. Health checks to remove unhealthy nodes automatically. |
| **App Servers (Mattermost Nodes)** | Stateless Mattermost server instances running behind the load balancer. Handle HTTP/WebSocket traffic. |
| **Shared Database (PostgreSQL/MySQL Cluster)** | Centralized database with replication for failover. Stores user profiles, messages, channel metadata. |
| **Redis Pub/Sub Cluster** | Enables real-time event propagation (e.g., new messages, typing indicators) across app servers. |
| **Shared File Storage** | File uploads are stored in shared NFS (Network File System) or S3-compatible object storage, ensuring consistency across all nodes. |

---

## 3. Key Features Enabled

### 3.1 Multi-Server Deployment
- **Session Stickiness** through Load Balancer (or Stateless Sessions with Token Auth).
- **Shared Database** ensures that all servers can see the same data.
- **Shared File Storage** ensures uploaded files are accessible across nodes.

### 3.2 Consistent Message History
- All write operations (sending messages, channel operations) go to the shared DB.
- Read replicas can be used for scaling read-heavy workloads.
- Database transactions ensure message ordering consistency.

### 3.3 Real-Time Synchronization
- Redis Pub/Sub broadcasts real-time events to all servers.
- WebSocket connections subscribe to Redis channels.
- User presence (online/offline/typing) is updated cluster-wide instantly.

---

## 4. Key Code Changes (Summary)

| Area | Changes Made |
|:-----|:-------------|
| **WebSocket Broadcast** | Modified WebSocket handling to support cluster-wide message and event broadcasts via Redis. |
| **Session Management** | Decoupled in-memory session storage and moved to Redis for centralized session tracking if needed. |
| **Cluster Event Bus** | Introduced a **Cluster Message Bus** abstraction using Redis Pub/Sub for server-to-server communication. |
| **Configuration Enhancements** | Added settings for enabling multi-node setup (`EnableCluster`, `RedisAddress`, etc.). |
| **File Storage Support** | Abstracted file storage layer to support shared storage (NFS, MinIO, etc.). |
| **Health Checks** | Exposed health endpoints (`/healthz`) for Load Balancer integration. |

---

## 5. Challenges and Solutions

| Challenge | Solution |
|:----------|:---------|
| WebSocket messages were not visible across different nodes. | Built a Redis Pub/Sub-based cluster message bus to broadcast WebSocket payloads to all app nodes. |
| Session invalidation was not cluster-aware. | Leveraged Redis-based session store or ensured stateless token-based session authentication. |
| Database writes under heavy load could bottleneck. | Planned for read replicas and database connection pooling (PgBouncer) to distribute load. |
| File upload consistency across servers. | Adopted shared file storage like NFS or MinIO (S3 API) so all servers have the same file view. |
| Deployment and operational complexity. | Added detailed documentation and scripts for setting up Redis clusters, DB replication, and load balancer configs. |

---

## 6. Configuration Steps (Summary)

### Application Settings:
```bash
# config.json
{
  "ClusterSettings": {
    "Enable": true,
    "UseIpAddress": true,
    "ReadOnlyConfig": true
  },
  "MessageExportSettings": {
    "ExportFromAllServers": true
  },
  "RedisSettings": {
    "Enable": true,
    "Password": "",
    "Host": "redis-cluster:6379"
  },
  "FileSettings": {
    "DriverName": "amazons3",  # or 'nfs'
    "AmazonS3Bucket": "mattermost-uploads",
    "AmazonS3Region": "ap-south-1",
    ...
  }
}
```

### Deployment Tips:
- Use **Sticky Sessions** (IP-hash) if WebSocket persistence is needed.
- Scale **Redis** using Sentinel or Cluster mode for HA.
- Database must have **Master-Replica setup** with automated failover.
- Set up **monitoring** using Prometheus + Grafana dashboards.
- Use **Backup strategies** for database and file storage.

---

## 7. Future Enhancements (Optional/Innovative)

- **Auto-scaling** app servers based on CPU/memory usage.
- **Service Mesh Integration** (e.g., Istio for traffic routing, retries).
- **Multi-Region Deployments** with eventual consistency.
- **Use NATS or Kafka** instead of Redis for better event streaming at massive scale.
- **Zero-downtime deployments** using blue-green strategies.

---

