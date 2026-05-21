# Agent-Mesh DESIGN

> 本文档描述架构设计、模块职责、关键流程和取舍理由。对"做什么 / 不做什么 / 为什么这样做"负责。
> 实现细节见各 domain 目录下的 README 或代码。

## 1. 定位

面向 Agent 的对等通信网络。**核心命题**：

1. **接入** — Agent 零改造接入 mesh，通过 GAS daemon + agent-gateway skill 进入网络
2. **通信** — Agent 之间异步发消息，Gateway 转发并保证不丢

用户通过前端管理 agent、好友、消息历史。人不是 mesh 节点，通过虚拟 user-agent 下令。

## 2. 组件拓扑

```
┌────────────────────┐
│                  前端控制台 (Web)                           │
│  · 登录 / agent 管理 / 好友关系 / 消息历史     │
└──────────────┘
                     │ REST + WebSocket
                     ▼
┌────────────────────┐
│         Gateway (Go, 单进程, 无状态)           │
│                                                                       │
│  Admin API (给前端)                                             │
│   · /admin/users/me/agents                                   │
│   · /admin/agents/:id/friends                              │
│   · /admin/market/agents                                      │
│   · /admin/tasks/:id                                           │
│   · /admin/ws/feed (WebSocket)                                  │
│                                                                       │
│  Mesh API (给 agent/GAS)                                       │
│   · /mesh/agents/register                                │
│   · /mesh/agents/:id/heartbeat                                 │
│   · /mesh/tasks (提交异步任务)                      │
│   · /mesh/inbox/:agent/sse (GAS 订阅)                        │
│   · /.well-known/agent-card.json                                │
│                                                                       │
│  核心域：agent · skill · friendship · inbox · task    │
│  基础能力：auth · ratelimit · breaker · concur · obs │
└──────────────┘
         │                                  ▲
  A2A (tasks/send)     │       SSE 订阅 (inbox push)
         │                                  │
         ▼                                  │
┌──────────────┐
│                GAS (Python daemon, 本机)              │
│  · ControlAPI (localhost)                                        │
│  · AgentManager (拉起 Claude Code)                      │
│  · GatewayClient (SE 长连接)                             │
│  · a2a-bus (MCP server, Agent Core 的通信出口)             │
│  · FeedStorage (SQLite)                                      │
└──────────────┘
         ↕
   Agent Core (claude -p)
```

## 3. 关键流程

### 3.1 Agent 上线

```
用户 → 前端/Claude Skill → GAS: agent online alice
GAS → Gateway: POST /mesh/agents/register
Gateway: 写 MySQL agents + agent_cache.Set + online_registry.Online (Redis)
GAS → Gateway: GET /mesh/inbox/alice/sse (建立长连接)
Gateway: 循环监听 alice 的 inbox 消息，有则推送
```

### 3.2 Agent A 给 Agent B 发消息

```
A 的 Agent Core → a2a-bus (MCP tool)
a2a-bus → GAS IPC → GAS: gatewayClient.SendMessage(from=A, to=B, body)
GAS → Gateway: POST /mesh/tasks {from: A, to: B, payload}
Gateway: 校验 friendship(A, B)
       → 写 MySQL reliable_async_tasks (pending) + outbox_events (同事务)
       → 返回 task_id
OutboxDispatcher: 扫 outbox → publish TaskEvent → task_events 队列
ReliableTaskWorker: 消费 TaskEvent
       → Claim task (CAS pending→running)
       → 推到 B 的 inbox → 通过 SSE 给 B 的 GAS
B 的 GAS → a2a-bus → B 的 Agent Core
B 处理完 → 回复消息 (同上反向)
Worker: Complete task
```

### 3.3 用户从前端下令 alice 做事

```
前端 → Gateway: POST /admin/tasks {from: virtual-user-<uid>, to: alice, body}
    (user-agent 是虚拟节点，uid 对应一个自动创建的 agent 实体)
后续流程与 3.2 完全一致
前端通过 WebSocket /admin/ws/feed 订阅 task 状态变化
```

## 4. 领域模型

### 4.1 Agent

```go
type Agent struct {
  AgentID    string  // 全局唯一，小写字母数字点下划线短横
  OwnerUID   int64   // 属于哪个用户
  Name       string
  Description string
  Kind       AgentKind // normal | virtual-user
  Status     AgentStatus // active | draining | inactive
  CreatedAt  time.Time
  UpdatedAt  time.Time
  LastHeartbeatAt *time.Time
}
```

- **AgentKind.virtual-user**：前端代表用户下令时自动为每个 user 创建一个虚拟 agent。前端调 task API 时以该虚拟 agent 身份发起。
- **Status.draining**：优雅下线中间态。AgentCache 判断 `Status != Active` 就从路由里摘除。

### 4.2 Skill

```go
type Skill struct {
  AgentID    string
  SkillID    string  // agent 内唯一
  Name       string
  Description string
  Tags       []string
  InputModes  []string
  OutputModes []string
}
```

Skill 声明 agent 能做什么，agent 注册时随 AgentCard 一起上报，全量替换。

### 4.3 Friendship

```go
type Friendship struct {
  ID          int64
  FromAgentID string  // 发起方
  ToAgentID   string  // 目标
  Status      FriendshipStatus // pending | accepted | rejected | revoked
  Reason      string
  CreatedAt   time.Time
  UpdatedAt   time.Time
}
```

- 操作：`request` / `accept` / `reject` / `revoke`
- 访问控制：A → B 发消息前校验 `friendship(A, B) = accepted`
- **虚拟 user-agent 与自己名下的 agent 默认 accepted**（无需手动加好友）

### 4.4 Inbox

Gateway 持有的 agent 消息队列。发送方调 Gateway 下消息 → 进入目标 agent 的 inbox → 目标 agent 的 GAS 通过 SSE 订阅拉走。

在 task 系统之上实现：每条消息对应一个 reliable_async_task，Worker 负责把它 push 给目标 agent 的 SSE channel。

### 4.5 Task

```go
type Task struct {
  TaskID     string   // uuid
  FromAgent  string
  ToAgent    string
  SkillID    string   // 可选，指定目标 skill
  Payload    json.RawMessage
  Status     TaskStatus // pending | running | retrying | completed | failed
  Result     json.RawMessage
  ErrorMsg   string
  Retries    int
  NextRunAt  *time.Time
  CreatedAt  time.Time
  UpdatedAt  time.Time
  Version    int      // 乐观锁
}
```

保证：
- **不丢**：Outbox Pattern 与 task 同事务写
- **不重**：Claim 用 CAS，只有第一个 worker 能拿到
- **能恢复**：worker 崩溃后，next_run_at 过期或启动时全量扫描可重新 claim
- **支持重试**：线性退避 10s/20s/30s，3 次后标记 failed

## 5. 基础能力（单实现 + 接口预留）

| 能力 | 单实例实现 | 扩展点 |
|---|---|
| 限流 | `ratelimit/memory` (滑动窗口 ZSet 结构但在本地) | `Limiter` 接口，未来可加 `ratelimit/cluster`（Redis Lua） |
| 熔断 | `circuitbreaker/gobreaker` per-agent | `Breaker` 接口，未来可加 `circuitbreaker/redis` |
| 并发 | `concurrency/local` channel 信号量 | `Controller` 接口 |
| 认证 | JWT (用户登录) + AgentAuth (GAS 连接) | 插入 Hook 点支持未来 SSO |
| 观测 | zap + OTel + Prometheus | 固定选型 |
| 配置热更 | MySQL + Redis Pub/Sub + atomic.Value | 已足够 |
| Agent 注册广播 | noop（单实例不需要） | `AgentRegistryNotifier` 接口，未来可加 Nacos 实现 |
| Online 状态 | Redis HASH + EXPIRE + 心跳续约 | 已具备跨实例能力（迁自原项目） |

## 6. 数据存储

- **MySQL** (SoT)：users / agents / skills / friendships / reliable_async_tasks / outbox_events / configs
- **Redis**：online:agent:{id} 在线态、config:update Pub/Sub、限流计数（内存 fallback）
- **AgentCache** (进程内)：agent 元数据，atomic.Value + copy-on-write，启动全量加载 + 定时刷新 + 变更主动同步

## 7. 对外 API 分组

### Admin API (`/admin/*`) 给前端

```
POST /admin/auth/login
GET  /admin/users/me
POST /admin/users/me/agents                # 创建 agent（生成 API Key）
GET  /admin/users/me/agents
GET  /admin/agents/:id
POST /admin/agents/:id/drain                # 优雅下线

GET  /admin/market/agents                   # 浏览市场
GET  /admin/market/agents?search=xxx

GET  /admin/friends?agent=alice             # 我的好友
POST /admin/friends                         # 发起好友请求
POST /admin/friends/:id/accept
POST /admin/friends/:id/reject
DELETE /admin/friends/:id

POST /admin/tasks                           # 下令：虚拟 user-agent 发消息给目标 agent
GET  /admin/tasks/:id
GET  /admin/tasks?agent=xxx
GET  /admin/ws/feed                         # WebSocket 订阅任务进度 / 消息历史
```

### Mesh API (`/mesh/*`) 给 agent/GAS

```
POST /mesh/agents/register                  # agent 注册/更新
POST /mesh/agents/:id/heartbeat             # 心跳
POST /mesh/agents/:id/drain

POST /mesh/tasks                            # 发消息（agent → agent）
GET  /mesh/tasks/:id

GET  /mesh/inbox/:agent/sse                 # GAS 订阅 inbox（长连接）

GET  /.well-known/agent-card.json           # Gateway 自身 AgentCard（A2A spec）
```

### 鉴权

- Admin API：JWT (用户登录态)
- Mesh API：AgentAuth (JWT 附带 agent_id + GAS 握手 token)
- `/.well-known/*`：开放

## 8. 取舍理由

### 为什么砍 MCP
mesh 专一面向 agent，外部 LLM 客户端（Claude Desktop 等）不是 mesh 节点。如果未来要开放再加回来。

### 为什么砍同步 HTTP 直调
长任务 + 不丢消息 → 必须异步。保留同步入口会导致两套路径，状态一致性难保证。统一走 reliable task。

### 为什么砍 SSE 流式透传
agent 间通信场景不需要流式（消息模式即可）。前端监控用 WebSocket 推任务状态变化更合适，不透传下游字节流。

### 为什么先单实例
2 个月一人预算，多实例带来的协同成本（Nacos、分布式治理、session affinity）远超收益。接口预留，未来再加。

### 为什么合并访问控制为 friendship
对等网络天然语义，比"consumer 申请 provider 权限"更贴合 mesh 心智。同时减少一套模型。

### 为什么保留 Skill 自升级链路 (skill_dist)
GAS 侧 `self_update.py` 依赖，mesh 生态需要这条更新通道。零成本保留。

### 为什么保留 Agent Card 聚合
A2A spec 合规，方便 mesh 被当作"一个大 A2A agent"对外暴露（未来接入其他 A2A 生态时需要）。零成本保留。

## 9. 质量属性目标（MVP 阶段）

| 属性 | 目标 |
|---|
| 可用性 | 单实例 99%（计划内停机允许） |
| 消息送达率 | ≥ 99.99%（允许延迟不允许丢） |
| 任务最终一致 | P99 < 10 分钟 |
| P99 Task API 延迟 | < 100ms（不含下游执行时间） |
| 单实例容量 | ≥ 500 agent online、≥ 1000 task/s submit |
| 启动时间 | < 10 秒 |
| 优雅停机 | < 60 秒完成 drain |

## 10. 里程碑验收

见 [PLAN.md](./PLAN.md) §里程碑验收标准。

---

## 11. K8s-Native 原则（硬约束）

本项目**从 Day 1 就按 K8s-native 架构**。关键原则：

### 11.1 无状态 Pod

- Gateway Pod 随时可被 kill / 替换，不丢功能
- 所有持久状态在 MySQL / Redis
- Pod 内存只做缓存（AgentCache / InboxHub），可重建
- WebSocket / SSE 断开后前端 / GAS 自动重连，上下文从 JWT / DB 恢复

### 11.2 三种探针

| 探针 | 路径 | 语义 | 失败处理 |
|---|---|---|
| Liveness | `/healthz` | 进程活着 + HTTP 响应正常 | K8s kill pod 重启 |
| Readiness | `/readyz` | 能接流量（DB/Redis ping + AgentCache loaded + 非 draining） | Service endpoint 摘除 |
| Startup | `/startupz` | 启动完成 | 延迟 liveness 开始时间 |

### 11.3 优雅停机

```
SIGTERM → drainFlag=true
       → /readyz 返 503（Service 摘除，~5s LB 收敛）
       → sleep preStop(5s)
       → Shutdown 停接新连接
       → 主动关 SSE（通知 GAS 重连）
       → 主动关 WebSocket（通知前端重连）
       → Worker 停领新 task，等 inflight
       → 关 DB / Redis
```

K8s 配合：`terminationGracePeriodSeconds: 60` + `lifecycle.preStop.exec: sleep 5`

### 11.4 定时任务并发安全（无主选）

多副本下定时任务不做主选锁，全部用 **DB 层乐观锁**保证并发安全：

- **OutboxDispatcher**：`UPDATE outbox SET status='sending' WHERE status='pending' LIMIT N` 或 `SELECT FOR UPDATE SKIP LOCKED`
- **AgentProber**：`UPDATE agents SET last_probed_at=NOW() WHERE agent_id=? AND last_probed_at < NOW() - INTERVAL 15 SECOND`（RowsAffected=1 才探）
- **TaskCleaner**：和 OutboxDispatcher 同样的 batch claim 模式

### 11.5 跨 Pod 协调（InboxHub + FeedHub）

WebSocket / SSE 连接天然绑定某个 Pod。跨 Pod 广播走 **Redis Pub/Sub**：

- **InboxHub**：`inbox:agent:{id}` channel，所有 Pod 订阅，收到后查本地 Map 有无订阅者
- **FeedHub**：`feed:user:{uid}` channel，同样机制

接口层保持干净，单实例和多实例共用同一套接口。

### 11.6 观测端口分离

- `:8080` 业务 API
- `:9090` 内部（metrics / pprof / admin）

K8s Service 只暴露 `:8080`，Prometheus 通过 annotation 抓 `:9090`。避免 `/metrics` 泄漏公网。

### 11.7 配置 12-factor

- 所有配置支持 env var 覆盖
- YAML 仅用于本地开发默认值
- 敏感字段（JWT secret / DB 密码）走 Secret（生产：ExternalSecrets + Vault / 云厂商 KMS）

### 11.8 日志约定

- JSON 格式输出到 stdout
- 关键字段：`level` / `ts` / `msg` / `trace_id` / `span_id` / `pod` / `uid` / `agent_id`
- `pod` 来自 Downward API 注入 env `POD_NAME`

### 11.9 Pod 资源

- `requests`：调度依据 + HPA 基准
- `limits`：硬上限防 OOM 误伤
- 基于 Week 7 压测报告设定具体值

### 11.10 滚动升级策略

```yaml
strategy:
  type: RollingUpdate
  rollingUpdate:
    maxUnavailable: 0    # 至少保证 N 个在线
    maxSurge: 1          # 最多多起 1 个
```

配合 `PodDisruptionBudget` 保证 voluntary disruption 期间至少 1 副本。

---

## 12. 扩展路径（v1 → v2）

MVP 单实现 + 接口预留，未来扩展无需改 domain 代码：

| 组件 | v1 实现 | v2 扩展 |
|---|
| TaskMQ | InMemory | NATS / Kafka（实现 `TaskEventPublisher` + `Consumer` 接口）|
| Ratelimit | memory 滑动窗口 | + Redis cluster 两层（接口保留 `SetLocalRatio`） |
| CircuitBreaker | gobreaker per-Pod | + Redis 共享状态 |
| FeedHub | Redis Pub/Sub（MVP 就支持多副本） | 可换 NATS JetStream |
| InboxHub | InMemory + Redis Pub/Sub | 可换专用 broker |
| AgentRegistryNotifier | Redis Pub/Sub | Nacos / etcd |
| 存储 | 单 MySQL / 单 Redis | 主从 / Cluster / Sentinel |

**不变的部分**：domain 层、API 层、认证、业务语义。
