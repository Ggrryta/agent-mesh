# Agent-Mesh 开发计划（K8s-Native 版）

> **2 个月 · 一人 · 每日开发 · K8s 原生部署**
>
> 目标：**从 MVP 到生产级**，9 周完整排期（含 Week 0 基建）。
> 版本：v3（基于 v2 的两点调整：①任务走扫表而非 Outbox，②群组通信从 roadmap 提前到 MVP）

---

## 1. 目标与约束

**目标**：在 2 个月内交付一个 K8s-native 的 Agent-Mesh MVP，同时达成：

1. 核心业务闭环：用户注册 → 创建 agent → 加好友 → mesh 内点对点 & 群组互发消息 → 前端监控
2. K8s 原生部署：多副本、滚动升级零丢失、自动伸缩、标准观测
3. 面向未来的扩展点预埋：多实例协调、分布式治理、跨服务发现

**约束**：
- 单人开发，每日全职
- 2 个月硬 deadline（~9 周，含 Week 0）
- 不依赖其他人协作 / 评审
- 生产环境用托管 K8s（云厂商），本地开发用 k3d

---

## 2. 核心原则

1. **K8s-native First**：从 Week 0 开始，所有开发、测试、验收都在 K8s 里进行。不存在"先单机跑通再上 K8s"的阶段。
2. **无状态 + 最终一致**：Gateway Pod 随时可替换，所有持久状态进 MySQL / Redis。
3. **单实现 + 接口预留**：治理组件只写一种实现，但接口要满足"未来加分布式实现不改业务代码"。
4. **每周可验收**：周末交付可在 K8s 跑的增量,拒绝"最后一起上线"。
5. **砍 ≠ 删**：原项目 `agent-gateway/` 保留参考，新代码独立在 `agent-mesh/`。
6. **文档同步**：代码动到哪，DESIGN / API / runbook / ADR 同步到哪。
7. **按语义选机制**：queue 语义（单消费者，如异步任务派发）走扫表 + CAS Claim；fan-out 语义（多消费者，如群消息广播）才走 Outbox + MQ。两条路径分开，不混用。
8. **中文注释 + 中文文档**：这是中文团队项目，所有代码注释、ADR、weekly、runbook 一律用中文。
   - **适用范围**：`.go` 文件里的 package doc / 函数 doc / 行内注释；`.sql` migration 注释；`.md` 文档；`.yaml` 配置里的 `#` 注释
   - **不强制**：标识符（函数名 / 类型名 / 变量名）保持英文；第三方库的 import / stdlib 错误字符串不翻译
   - **例外**：ADR / 文档里引用英文术语（JWT / CAS / Outbox / TaskState 等）保持英文，不音译
   - **历史代码**：Week 0-1 用英文注释写的代码，后续维护时顺手翻译即可，不专门做一轮重写

---

## 3. K8s-Native 硬约束（所有 Week 都遵守）

这些是**架构层面的不可商量项**，Day 1 就要做对：

| 约束 | 做法 |
|---|
| 探针分三种 | `/healthz` (liveness) / `/readyz` (readiness) / `/startupz` (startup) |
| 配置走 env | 全部配置项支持环境变量覆盖，yaml 仅本地默认值 |
| 日志 stdout + JSON | zap `NewProductionConfig` + `Encoding: json` + `OutputPaths: stdout` |
| Metrics 端口分离 | `:8080` 业务 / `:9090` 管理（metrics / pprof / admin）|
| 优雅停机 | SIGTERM → `/readyz` 返 503 → sleep preStop → Shutdown → 等 inflight |
| 连接池可配 | MySQL / Redis / HTTP Client 的池大小走配置，便于按副本数倒推 |
| 无本地状态 | 不写文件、不靠内存 session，所有状态外置 |
| 定时任务并发安全 | 用 DB `SELECT ... FOR UPDATE SKIP LOCKED` 或时间戳 CAS，禁止主选锁 |
| 敏感配置走 Secret | JWT secret / DB 密码 / Redis 密码必须从环境变量注入 |
| Dockerfile 最小化 | distroless / static image，无 shell 无包管理器 |

---

## 4. 砍法清单（决定版）

### ✂️ 不迁移

业务模型：`agent_apply` / `visibility` / `consumer` / `apikey`（用 JWT + Friendship 统一）
协议入口：`mcp.go` / `a2a_proxy.go` / 同步 HTTP 直调 / SSE 流式透传
任务事件流：**不再把任务和 Outbox 耦合**——原项目里任务也经 `outbox_events`（`AsyncTaskCreated` / `AsyncTaskRetry` 两种事件）属于过度设计。任务是 queue 语义（单 Worker 消费），用扫表 + Claim 就够，不需要 Outbox；Outbox 留给真正的 fan-out 场景（群消息 / 广播事件，见 Week 4）。
冗余实现：Redis 版 `task_worker` / `kafka_task_queue`（原项目有两套异步实现，砍掉 Redis 版）
分布式治理实现：`ratelimit/cluster` + `hybrid` + `token_bucket` / `circuitbreaker/redis` / `concurrency/distributed` + `hybrid`
外部依赖：`pkg/nacos/` + `nacos_watcher.go`（单实例不需要注册中心）
其他：`canary.go` / `object_pool.go` / 旧版 `client-skill/gateway.py`

### ✅ 迁移（保留核心）

- Agent 注册 / 健康 / cache / prober（改 DB 时间戳分片）
- Skill 声明
- Friendship（唯一访问控制）
- Inbox Hub
- **Reliable Async Task（扫表版，不带 Outbox）** —— 原项目 `reliable_task_worker` 里 `scanRunnableTasks` + `Claim` 这部分是核心，保留。`outbox_events` 对任务这一路径只是重复兜底
- Online Registry
- JWT Auth + Agent Auth
- memory ratelimit / gobreaker / local concurrency
- config watcher（MySQL + Redis Pub/Sub）
- zap / otel / prometheus
- `/.well-known/agent-card.json` + `skill_dist`（GAS 自升级依赖）
- GAS / agent-gateway skill / AGW CLI

### 🆕 新增

- User 模型 + JWT 登录
- Virtual user-agent（人 → mesh 的桥）
- Admin API（给前端）+ Mesh API（给 agent/GAS）分组
- **群组通信底座（原 v2 roadmap 项，本版本提前到 MVP）**
  - 群表 / 成员表 / 消息表
  - `outbox_events` 表 + OutboxDispatcher：**只服务于群消息 / 广播事件**，不混其它
  - `TaskEventPublisher` 接口 + InMemory 实现（留换 NATS / Kafka 的接口点）
  - 多消费者 fan-out：发送方写库 + outbox，Dispatcher 发 MQ，各实例的 InboxHub 订阅后 push 给在线成员
- WebSocket FeedHub（基于 Redis Pub/Sub 直接做多实例版）
- Frontend 控制台
- **K8s 全套部署制品**（Dockerfile + Helm chart + CI 流水线）
- ADR 目录（记录关键架构决策）

---

## 5. 9 周开发计划

### Week 0：K8s 基建 + 项目脚手架（5 天）

**目标**：本地 k3d 集群跑起来，能部署一个 "Hello Agent-Mesh" Pod，CI 能构建镜像。

- **Day 0.1** k3d + 工具链
  - 安装 k3d / kubectl / helm / k9s / stern
  - 本地建 `agent-mesh-dev` 集群（1 server + 2 agent nodes）
  - 验证 `kubectl get nodes`
- **Day 0.2** Go 项目脚手架
  - `gateway/go.mod`（Hertz + gorm + go-redis + zap + otel + prometheus）
  - `cmd/server/main.go` 打印 "Hello Agent-Mesh" + 三个探针 `/healthz /readyz /startupz`
  - 配置走 env（`GATEWAY_PORT` / `LOG_LEVEL`），yaml 备份
- **Day 0.3** Dockerfile + 本地构建
  - 多阶段 build：go builder + distroless runtime
  - `docker build` + `docker run` 本地验证
  - 镜像推到 k3d 内嵌 registry
- **Day 0.4** 最简 K8s 清单
  - `deploy/k8s/base/` 目录：Deployment / Service / ConfigMap / Secret
  - replicas=2，三种探针配齐
  - Ingress（nginx-ingress）暴露到本地 `http://mesh.localhost`
  - `kubectl apply -k deploy/k8s/base`
- **Day 0.5** CI 流水线 + 验收
  - GitHub Actions / GitLab CI：build → test → docker push → kubectl apply 到 dev 集群
  - `helm template` 试跑
  - 验收：`curl http://mesh.localhost/healthz` 返回 200，kill 一个 Pod 另一个 Pod 继续响应

**里程碑 0**：一条命令 `make dev` 起 2 副本 K8s 集群 + 服务能响应，Pod 挂了 30s 内自动拉起。

---

### Week 1：User 模型 + Agent 注册 + DB 层（7 天）

**目标**：用户能注册登录，能通过 API 创建 agent，agent 能注册到 Gateway。

- **Day 1** 数据层 + migration 工具
  - `cmd/migrate/main.go`（基于 goose / sql-migrate）
  - Migration 文件：`users` / `agents` / `skills` / `friendships` / `reliable_async_tasks` / `outbox_events` / `configs` 基础表结构
  - `infra/mysql/` 封装：GORM + 连接池 + 健康检查
  - `infra/redis/` 封装：go-redis + 健康检查
  - `/readyz` 接入 DB + Redis ping
- **Day 2** User 域 + JWT
  - `domain/user`：Register / Login / GetMe
  - JWT 签发（HS256）+ Claims（uid / username / exp）
  - `middleware/auth.go`：用户 JWT 解析
  - Admin API：`POST /v1/admin/auth/register` / `POST /v1/admin/auth/login`
- **Day 3** Agent 域核心
  - `domain/agent/model.go` + `repo.go`
  - `domain/agent/service.go`：Register / Deregister / Drain
  - `domain/agent/cache.go`：`atomic.Value` + copy-on-write + 启动全量
  - Virtual user-agent：User 注册时自动创建 `virtual-user-<uid>`
- **Day 4** Agent API
  - Admin API：`POST /v1/admin/users/me/agents`（创建 agent，返回 agent JWT）
  - Admin API：`GET /v1/admin/users/me/agents`
  - Admin API：`POST /v1/admin/agents/:id/drain`
  - Mesh API：`POST /v1/mesh/agents/register`（agent JWT）
  - Mesh API：`POST /v1/mesh/agents/:id/heartbeat`
  - `middleware/agent_auth.go`：agent JWT + `X-Agent-ID` 归属校验
- **Day 5** Skill 域
  - `domain/skill`：ReplaceByAgentID（全量替换）
  - Agent 注册时随 AgentCard 一起上报 skill
- **Day 6** Agent Prober
  - `domain/agent/prober.go`：并发安全版
  - 改用 DB 时间戳分发：`UPDATE agents SET last_probed_at=NOW() WHERE agent_id=? AND last_probed_at < NOW() - INTERVAL 15 SECOND`
  - RowsAffected=1 才探，天然支持多副本
  - 3 次失败 → `inactive`，恢复 → `active`
- **Day 7** 集成测试 + 缓冲
  - k3d 集群里跑 e2e：注册 → 登录 → 创建 agent → agent 自注册 → prober 探活
  - 2 副本验证：轮询多副本时数据一致

**里程碑 1**：k3d 里用 curl 走完"注册 → 登录 → 建 agent → 查 agent → prober 探活"全流程，两副本表现一致。

---

### Week 2：Friendship + Market + Admin API 整合（7 天）

**目标**：两个用户可互加好友，market 可浏览 agent。

- **Day 1** Friendship 域
  - `domain/friendship`：Request / Accept / Reject / Revoke
  - DB：`(from_agent, to_agent)` pending 状态唯一索引防重
  - 隐式 accepted：virtual-user-agent 和 owner 名下 agent 默认互为好友
- **Day 2** Friendship API
  - `POST /v1/admin/friends`
  - `POST /v1/admin/friends/:id/accept`
  - `POST /v1/admin/friends/:id/reject`
  - `DELETE /v1/admin/friends/:id`
  - `GET /v1/admin/friends?agent=xxx`
- **Day 3** Market
  - `GET /v1/admin/market/agents`（可选 keyword / tag 过滤）
  - 分页 + 排序（按创建时间）
- **Day 4** 错误规范化 + 中间件完善
  - 统一错误码（见 docs/api.md）
  - `middleware/request_id`：生成 request_id 写响应 header
  - `middleware/access_log`：结构化访问日志
  - `middleware/recover`：panic 恢复
- **Day 5** 单元测试补齐
  - User / Agent / Skill / Friendship 核心路径 ≥ 70% 覆盖
  - `go test ./... -race` 通过
- **Day 6** 压测环境准备
  - `test/load/` 下写 k6 / vegeta 脚本
  - Agent 注册 / Friendship 建立 / Admin API 基础压测
- **Day 7** 缓冲

**里程碑 2**：Alice 和 Bob 两个用户互加好友成功，market 能搜到彼此。

---

### Week 3：Reliable Task（消息中枢，A2A 对齐，5 天 + 2 天缓冲）

**核心定位**：Gateway **不执行任务**，不调 agent，不做 orchestration。Gateway 是
**消息中枢**：持久化 Task 事实、路由消息与产物、保证离线可达。Task 的业务执行
和状态推进由两端 agent 各自完成。详见 ADR 002 / ADR 004 / ADR 010。

**目标**：Alice 的 agent 通过 Gateway 把 Task 消息送达 Bob 的 agent；Bob 执行
过程中产生的状态变更、artifact、回复 message 经 Gateway 回到 Alice；agent
任意一方短暂离线不丢消息。

**设计原则**：
- **Gateway 不占任务**：无 Worker、无 Claim、无重试、无孤儿回滚。Alice/Bob
  自己驱动状态机，Gateway 只校验状态转换合法性
- **真相之源 = 三张表**：`reliable_async_tasks` + `task_messages` + `task_artifacts`
  （migration 0001 已落地）
- **送达两条路径**：inbox 持久化（MUST）+ push 尝试（OPTIONAL，对登记了 URL 的 agent）
- **幂等**：`message_id` / `artifact_id` UNIQUE 索引，重复提交返回已存在记录

- **Day 1** Task/Message/Artifact 领域模型
  - `domain/task/model.go`：
    - `TaskState` 对齐 A2A：`submitted | working | input-required | auth-required | completed | canceled | failed | rejected`
    - 状态机：合法转换矩阵内置，`TransitionStatus(from,to)` 做运行时校验
    - `Message` / `Artifact` / `Part`（Part 支持 text / raw / url / data 四种 oneof）
  - `domain/task/repo.go`：
    - `CreateTask(task, firstMessage)` —— 事务内 INSERT tasks + INSERT 首条 message
    - `AppendMessage(taskID, message)` —— 追加消息；`message_id` UNIQUE 保证幂等
    - `AppendArtifact(taskID, artifact)` —— 追加 artifact；`(task_id, artifact_id)` UNIQUE
    - `TransitionStatus(taskID, fromStates[], toState, errMsg)` —— `UPDATE ... WHERE status IN (...) AND RowsAffected=1`，并发安全
    - `GetTask(taskID, withHistory, withArtifacts)` / `ListByContext(contextID)`
  - `domain/task/service.go`：
    - 发送前校验：`from_agent_id` 属于 caller、`friendship.AreFriends(from, to)=true`、`to.kind != virtual-user`
    - 状态机合法性：内置一张 `allowedTransitions[from][]to` 表
    - Gateway 只允许 **agent 自己** 推状态；不主动改任何 task 状态
- **Day 2** Inbox + 送达
  - `domain/inbox/`：
    - 新增 migration 0003：`inbox_events` (id, agent_id, kind, task_id, ref_id, payload_json, created_at, delivered_at, KEY idx_agent_undelivered)
    - kind ∈ {message | artifact | transition}；ref_id 对应 message_id / artifact_id / 状态 to_state
    - 核心方法：`Enqueue(agentID, event)` / `ListUndelivered(agentID, sinceID, limit)` / `MarkDelivered(ids)`
  - `internal/delivery/push.go`（轻量 push 尝试）：
    - 输入：inbox event
    - 行为：若 to_agent 在 agents 表登记了 URL，尝试 HTTP POST 到 `{url}/a2a/events`
    - 成功 → `MarkDelivered`；失败 → 留 inbox，等 agent 来拉或 SSE 订阅
    - **不做重试**（下次 agent 来拉自然会看到；或下次 task 动作触发时再次 push）
    - push 是后台 goroutine 处理，API 请求不等 push 完成
- **Day 3** Mesh API（全部 agent JWT 保护）
  - `POST /v1/mesh/tasks`
    - body: `{to_agent_id, context_id?, message:{message_id, parts}}`
    - Gateway：授权校验 → 写 task + 首条 message → 入 Bob inbox → 触发 push 尝试
  - `POST /v1/mesh/tasks/{id}/messages`
    - body: `{message_id, parts}` role 由 caller 在 task 中的身份推断（from=自己=user，to=自己=agent 回复）
    - 幂等：`message_id` 已存在直接返回 200
    - 入对方 inbox + push
  - `POST /v1/mesh/tasks/{id}/artifacts`
    - body: `{artifact_id, name?, parts}`
    - 仅 to_agent（serving agent）可调；校验 caller = to_agent_id
    - 入 from inbox + push
  - `POST /v1/mesh/tasks/{id}/transition`
    - body: `{to_state, error?}`  agent 驱动状态机
    - Gateway 校验转换合法性 + owner（发送方能转 canceled；接收方能转 working/input-required/completed/failed/rejected 等）
    - 入对方 inbox + push
  - `GET /v1/mesh/tasks/{id}?include=history,artifacts`
    - 双方可读；`AreFriends` 判定已在 Submit 阶段完成，这里只查 owner
  - `GET /v1/mesh/tasks?context_id=xxx`
    - 按 context 聚合；只返回自己参与的 Task
  - `GET /v1/mesh/inbox?since={event_id}&limit=N`
    - 长轮询拉 inbox；返回 events 数组 + max_id（下次传入作为 cursor）
    - SSE 端点留 Week 4 做
- **Day 4** 装配 + 端到端 E2E
  - `cmd/server/main.go` 接 task / inbox / push worker
  - E2E 脚本：起两个 httptest agent（alice-fake / bob-fake）模拟：
    - alice 发 task → bob inbox 收到 → bob 汇报 working → bob 追加 artifact → bob 汇报 completed → alice inbox 收到全部事件
    - input-required：bob 返回 input-required + 追加 agent message → alice inbox 收到 → alice 追加 user message → bob inbox 收到 → bob 继续
    - cancel：alice 汇报 canceled → bob inbox 收到
  - 幂等重试：相同 message_id 提交两次，DB 只一行
- **Day 5** Observability + ADR
  - Prometheus：`task_submit_total` / `task_message_append_total` / `task_artifact_append_total` /
    `task_transition_total{from,to}` / `inbox_enqueue_total` / `push_attempt_total{result}` /
    `friendship_denied_total`（按 reason）
  - zap 结构化：`task_id` + `context_id` + `message_id` 全链路贯穿
  - OTel trace：Submit → Enqueue inbox → Push attempt；Append → Enqueue → Push
  - 完成 `docs/adr/002-gateway-as-hub.md`：Gateway 不执行任务的根因决策
  - 完成 `docs/adr/004-a2a-task-model.md`：三表对齐 A2A
  - 完成 `docs/adr/010-delivery-model.md`：inbox + 可选 push 的送达模型
- **Day 6-7** 缓冲 + Week 4 Inbox SSE 提前

**里程碑 3**：
- `POST /v1/mesh/tasks` 提交 → Bob inbox 立即可读；有 URL 时 push 到 Bob 的 `/a2a/events`
- Bob 汇报状态 / artifact → Alice inbox 立即可读
- 完整 input-required 多轮对话跑通
- cancel 任意阶段能中止（转 canceled 合法性校验通过即可）
- 同一 message_id 重复提交，DB 保持一行（幂等）
- `friendship.AreFriends=false` 的请求被拒（40302）
- `to_agent_id.kind=virtual-user` 的请求被拒（40001，"任务不反抛给用户"）
- agent 短暂离线恢复后，用 `GET /inbox?since=...` 能拿到全部积压事件
- 同 context_id 下 `GET /tasks?context_id=X` 返回完整对话 Task 列表

### Week 4：Inbox + 群组消息底座 + Outbox + GAS 接入（7 天）

**目标**：Alice 的 Agent Core 给 Bob / 给群组发消息，成员的 GAS 能 SSE 收到。**Outbox 在本周首次引入，服务消息 fan-out**。

**设计原则**：
- 消息是 **fan-out 语义**（一条群消息多个成员收），**必须走 Outbox + MQ**
- 没有 Outbox 时，"写 messages 表后发 MQ 失败"会导致部分成员漏收消息
- `TaskEventPublisher` 抽象在这里落地，为未来换 Kafka 铺路
- **群组消息复用 task_messages 表**：不再独立建 messages 表。群 = `contextId`，群成员都能看到同 context 下的消息；群消息 = `role=user|agent` 的 task_messages 行 + metadata.group_id

- **Day 1** Inbox Hub（单 Pod 内实现）
  - `domain/inbox/hub.go` 接口 `Publish(agentID, msg) (delivered bool, err error)` / `Subscribe(agentID) (<-chan Msg, func())`
  - InMemory 实现：`map[agentID][]chan Msg`
  - Worker 执行点对点 task 时调用 `hub.Publish(toAgent, msg)`，返回 false 则走重试
- **Day 2** 群组模型（不建 messages 表）
  - Migration 0002：`groups` / `group_members` / `outbox_events`（outbox 本周首次加）
  - `groups` 字段：id / group_id / context_id（1:1 关联一个 A2A context）/ name / owner_uid / created_at
  - `group_members`：group_id / agent_id / role (owner|admin|member) / joined_at
  - **群消息不建独立表** —— 用 `task_messages.metadata_json.group_id` 标记 + `context_id = group.context_id`
  - `domain/group`：Create / AddMember / RemoveMember / ListMessages(groupID, since)
  - 发群消息事务：`INSERT task_messages + INSERT outbox_events`（event_type='group_message'，payload 含 group_id + message_id + context_id）
- **Day 3** OutboxDispatcher + TaskEventPublisher 抽象
  - `domain/outbox/dispatcher.go`：每 1s 扫 `outbox_events WHERE status='pending'`，带 `FOR UPDATE SKIP LOCKED`
  - `TaskEventPublisher` 接口 + `InMemoryTaskQueue` 实现（Go chan）
  - Dispatcher 成功发送后 `MarkSent`；失败退避重试（5s/10s/...），10 次 → `MarkFailed`（进 DLQ）
  - 关键验证：**两副本同时跑 Dispatcher 不重复发**（靠 SKIP LOCKED）
- **Day 4** Inbox 跨 Pod 路由 + 群消息 fan-out
  - 群消息场景：Alice 发到 group_42 → Dispatcher 发事件到 MQ → **每个 Gateway 实例的 InboxPusher 消费事件** → 查 group_members → 本实例有哪些成员的 SSE 在线 → 本地 push
  - `DistributedHub` 实现：用 Redis Pub/Sub 广播 `inbox:agent:{id}` 作为点对点路径
  - InMemory Hub 作为测试替身保留
- **Day 5** Inbox SSE + Online Registry + GAS 迁移
  - `GET /v1/mesh/inbox/:agent/sse` SSE 长连接 + OnlineRegistry 上/下线
  - 30s ping 保活
  - GAS 从 `agent-gateway/gas/` 迁过来：改 Gateway URL（env）、改 API 路径（`/a2a/*` → `/v1/mesh/*`）、SSE 断线重连带指数退避
- **Day 6** 端到端联调 + 滚动升级验证
  - 场景 1：Alice → Bob 点对点 Task（task_messages 承载对话，task_artifacts 承载产物）
  - 场景 2：Alice → group_42 广播（task_messages + metadata.group_id + outbox fan-out）
  - Drain 流程：`/readyz`→503 → preStop sleep 5s → Shutdown → 主动关 SSE → Worker 停新 task 等 inflight
  - `kubectl rollout restart deployment/gateway` 滚动期间消息 0 丢失
- **Day 7** 缓冲 + ADR
  - 写 `docs/adr/003-outbox-for-fanout.md`：为什么群消息走 Outbox 而不是直发
  - 写 `docs/adr/005-mq-abstraction.md`：InMemory → NATS / Kafka 的适配路径

**里程碑 4（关键）**：
- k3d 2 副本 Gateway
- 点对点：Alice 和 Bob 跨 Pod 互发消息成功；任务产出 artifacts 能查
- 群组：5 个 agent 加入 group_42，任一成员发消息后另外 4 个都收到（fan-out 验证）
- `rollout restart` 滚动期间消息 0 丢失
- Dispatcher 并发安全：双实例下发事件无重复

---

### Week 5：治理层 + 观测 + 群组 API + FeedHub（7 天）

**目标**：限流 / 熔断 / 并发 / 超时 / 观测全装上；群组 CRUD + 消息收发 API 完整；FeedHub 跨 Pod 广播就绪。

- **Day 1** 限流
  - `pkg/ratelimit/limiter.go` 接口 + `memory.go`（滑动窗口 ZSet 本地版）
  - middleware 接入 mesh API 层：per-agent 限流
  - 接口预留 `SetLocalRatio` 方法（未来 Hybrid 本地 + Redis 两层用）
- **Day 2** 熔断
  - `pkg/circuitbreaker/breaker.go` 接口 + `gobreaker.go`
  - `AgentCallGuard` 懒初始化 per-agent
  - Task Worker 执行下游时走 Guard
- **Day 3** 并发 + 超时
  - `pkg/concurrency/local.go` channel 信号量
  - HTTP Client 超时 + ctx.WithTimeout 贯穿 handler / worker
- **Day 4** 群组 API + 消费者完整化
  - Admin API：
    - `POST /v1/admin/groups` 创建群
    - `POST /v1/admin/groups/:id/members` 加成员
    - `DELETE /v1/admin/groups/:id/members/:agent` 移除
    - `GET /v1/admin/groups/:id/messages?since=...` 拉历史
  - Mesh API：`POST /v1/mesh/groups/:id/messages`（agent 发群消息，事务写 messages + outbox）
  - InboxPusher 消费 `group_message` 事件：查成员列表 → 每个成员检查本地在线 SSE → push；离线成员消息已入 `messages` 表，下次连接拉历史
  - 幂等：`message_id` 唯一索引，重复事件 push 时去重
- **Day 5** FeedHub（WebSocket 跨 Pod 广播，**K8s 必需**）
  - `domain/feed/hub.go` 接口：`BroadcastToUser(uid, event)` / `Register(uid, conn) unreg`
  - 直接实现 Redis Pub/Sub 版（不做 InMemory-only 版本，多副本是默认）
  - 每 Pod 订阅 `feed:user:{uid}` channel
  - Handler：`GET /v1/admin/ws/feed`（WebSocket）
  - 连接无状态：只存 uid → conn 映射，订阅关系从 JWT / DB 推导
  - FeedHub 事件类型：task.updated / agent.status / friendship.updated / **group.message**（前端显示群聊实时消息）
  - 断线重连：前端自动 backoff（Week 6 前端做）
- **Day 6** Observability 体系 + Grafana
  - zap JSON + trace_id / pod / uid / agent_id 字段约定
  - OTel HTTP middleware + outgoing HTTP client propagation
  - Prometheus metrics：补齐 online_agent_count / inbox_queue_depth / breaker_state / ratelimit_reject / **outbox_pending_depth / outbox_publish_latency / task_claim_conflict**
  - `deploy/grafana/dashboards/agent-mesh.json`：请求 QPS / 延迟 / 错误率、task 成功率 / 堆积、online agent 数、outbox 堆积、breaker 状态、资源用量
  - Alertmanager 规则：task 失败率 > 5% / online 骤降 > 30% / breaker 打开 > 5min / Pod OOM / **outbox 堆积 > 1000 持续 5min**
  - 每个告警附 runbook 链接
- **Day 7** 缓冲 + 故障演练
  - 故意停 MySQL / Redis → 观察降级行为
  - HPA 配置（CPU > 70% 扩容），验证压力下自动扩

**里程碑 5**：Grafana 仪表盘完整，告警能触发（含 outbox 堆积），HPA 能扩缩，降级行为符合预期，**群组消息 API 可用**。

---

### Week 6：前端控制台（7 天）

**目标**：用户 100% 通过浏览器完成所有 mesh 操作。

- **Day 1** 脚手架 + 登录
  - Vue 3 + Vite + Element Plus（或 React + AntD，按熟悉度）
  - Pinia 状态管理、axios + 拦截器（JWT 注入 + 401 刷新）
  - 登录 / 注册页
- **Day 2** Agent 管理页
  - 我的 agent 列表 / 创建 / drain / 删除
  - API Key 首次展示弹窗（仅一次可见）
- **Day 3** Market + 好友关系 + 群组管理
  - Market 搜索 + 加好友申请
  - 好友列表 + pending 审批（Inbox）
  - 群组列表 / 创建群 / 加成员 / 退出
- **Day 4** Task 监控页
  - Task 列表（筛选 + 分页 + 实时刷新）
  - Task 详情（payload / result / 状态流转时间线）
- **Day 5** WebSocket 实时推送 + 群聊视图
  - `src/ws/feed.ts`：AutoReconnectWS（exponential backoff + 连接状态指示）
  - 对接 `/v1/admin/ws/feed`，收 task.updated / agent.status / friendship.updated / group.message
  - 页面订阅后自动更新
  - 群聊页：左侧群列表，右侧消息流（实时 + 历史拉取），发送框走 `POST /v1/admin/groups/:id/messages`
- **Day 6** "下令"交互
  - 在 agent 详情页输入指令
  - 调 `POST /v1/admin/tasks`（from=virtual-user-<uid>, to=<agent>, body）
  - 实时看到 task 进度 + 返回结果
- **Day 7** 前端 Dockerfile + K8s 清单 + 缓冲
  - nginx + 静态资源 Dockerfile
  - `deploy/k8s/base/frontend.yaml`
  - Ingress 路由分流：`/` → frontend，`/v1/*` → gateway

**里程碑 6**：k3d 集群里打开 `http://mesh.localhost` 能走完所有 mesh 操作，不碰 CLI。

---

### Week 7：测试 + 稳定性 + Helm Chart（7 天）

**目标**：全链路测试，压测基线，故障演练，生产级 Helm chart。

- **Day 1** 全链路集成测试
  - `test/e2e/*`：注册 → 登录 → 建 agent → 加好友 → 提交 task → 看消息 → drain → 重连
  - 用 Testcontainers / Kind 起完整环境
  - CI 里每次 PR 跑
- **Day 2** 压测 + 容量基线
  - k6 脚本：Task 提交 QPS、Inbox SSE 订阅数、WebSocket 连接数、**群组 fan-out 场景**（100 成员群按 10 msg/s 发，观察 Outbox 堆积、消息送达率）
  - 在 2 副本 Gateway + 2 core 4G 环境下跑
  - 报告：`docs/benchmarks.md`（QPS / P50 / P99 / 资源占用 / 群组 fan-out 放大系数）
  - HPA 阈值基于压测数据设定
- **Day 3** Chaos 演练
  - `kubectl delete pod`（kill Gateway）→ inflight task 不丢
  - MySQL down → Gateway /readyz 返 503，不接流量
  - Redis down → 限流 / feed 降级到 noop / 本地
  - 网络分区（netem）→ Prober 超时后下线 agent
  - 写 `docs/chaos-report.md`
- **Day 4** 数据治理 + 定时任务
  - Outbox MarkSent 后归档 cron（7 天前的搬到 archive 表）
  - Task 完成后结果保留 7 天（可配），过期清理
  - Redis 内存告警
  - 定时任务用 K8s CronJob 部署，共享同一镜像
- **Day 5** Helm Chart
  - `deploy/helm/agent-mesh/` 完整 chart
  - `values.yaml` 支持：replicas / image / resources / ingress / externalSecrets / monitoring
  - 子 chart 或 dependency：prometheus-stack（可选）
  - `helm install` / `helm upgrade` / `helm rollback` 全流程演练
- **Day 6** 文档体系
  - `docs/concepts.md`（已有）补充流程图
  - `docs/api.md`（已有）补完
  - `docs/deployment.md`：local k3d / 生产 K8s / Helm / ExternalSecrets 对接
  - `docs/runbook.md`：启动 / 升级 / 回滚 / 常见故障排查 / 告警响应
  - `docs/adr/*`：记录关键决策（001-k8s-native / 002-task-poll-vs-outbox / 003-outbox-for-fanout / 004-a2a-task-model / 005-mq-abstraction / 006-feed-hub-redis-pubsub 等）
- **Day 7** 版本化 + 缓冲
  - API 版本约定：新字段追加兼容、破坏性变更走 `/v2/`
  - Gateway / GAS / Skill 版本兼容矩阵表

**里程碑 7**：Helm chart 一条命令起全栈，压测报告完整，chaos 演练通过，文档完备。

---

### Week 8：生产上线 + 打磨（7 天）

**目标**：内部 beta 上线，跑真实场景，修关键问题。

- **Day 1** 安全审计
  - Secret 全部走 ExternalSecrets（对接 Vault / 云厂商 KMS）
  - 日志脱敏：JWT / API Key / 消息 payload 敏感字段
  - CORS / CSRF / XSS 检查
  - 所有公开入口 rate limit 覆盖
  - 镜像漏洞扫描（Trivy）
  - 写 `docs/security.md`
- **Day 2** 生产部署准备
  - 生产 K8s 集群接入（云厂商 EKS / GKE / ACK）
  - 网络策略（NetworkPolicy）:只允许 Gateway 访问 MySQL / Redis
  - PDB（PodDisruptionBudget）保证滚动升级期间至少 1 副本
  - HPA 实际部署 + 验证扩缩
- **Day 3** CI/CD 完善
  - PR 流程：lint / test / build / integration test on kind
  - Merge 到 main → 构建镜像 → 部署到 staging
  - 手动确认 → 部署到 prod
  - Argo CD / Flux 对接（可选，GitOps）
- **Day 4** 体验打磨
  - 前端 loading / 空状态 / 错误 toast 完善
  - GAS 一键安装脚本更新
  - AGW CLI 帮助文档
  - 错误消息更友好
- **Day 5-6** 内部 beta
  - 找 1-2 个同事用真实场景
  - 每日收集 bug，高优必修
  - 性能回归跟踪
- **Day 7** Release Notes + 复盘
  - `docs/release-notes/v1.0.0.md`
  - 总结：做了什么 / 砍了什么 / 遗留问题 / v2 roadmap

**里程碑 8（最终）**：生产 K8s 集群跑 2 副本 Agent-Mesh，内部 beta 用户顺利使用，关键 bug 修完，DESIGN §质量属性目标全达成。

---

### Week 9：meshd 本机服务化 + 内嵌 UI（7 天，ADR 014）

**目标**：用户体验从"装命令行 + 配 env + 跑进程"升级到"装 app + 浏览器一站式"。Gateway 后端零改动作为中心，meshd 是本机执行层 + UI 宿主。

- **Day 1（M1.1）** gas-ts → meshd 重构：HTTP server + 多 worker
  - `gas-ts/` 目录改名 `meshd/`
  - 引入 Hono（Bun 原生 HTTP 框架）监听 `127.0.0.1:7878`
  - `AgentRuntime` 改为支持多实例：`Map<agentID, AgentRuntime>`
  - 加 `~/.agent-mesh/state.json`，记录哪些 agent 设置了 `auto_start`
  - 启动时读 state.json，对每个 auto_start 自动起 worker
  - `~/.agent-mesh/cursor/{agent_id}` 取代当前的 `~/.agent-mesh/{agent_id}/cursor`
- **Day 2（M1.2）** 凭证 + device-flow 登录
  - 系统 keychain 集成（macOS Keychain / Linux libsecret / Windows DPAPI）
  - device-flow 登录：meshd 调 Gateway `POST /v1/admin/auth/device/start` 拿 user_code → 弹 URL → 轮询 `POST /v1/admin/auth/device/poll` 拿 user JWT
  - Gateway 加 device-flow 端点（短期 code → user JWT 兑换）
  - localhost API 鉴权：随机 token cookie，`agent-meshd open` CLI 读 token 拼 URL 启动浏览器
- **Day 3（M1.3）** 单二进制 + 简单进程管理
  - `bun build src/index.ts --compile --outfile dist/agent-meshd-{platform}`
  - 跨平台编译（macOS arm64/x64、Linux x64/arm64、Windows x64）
  - CLI 子命令：`start` / `stop` / `restart` / `status` / `open` / `logs` / `run` —— 用户感知的简单进程管理（fork+detach，pid 写 runtime.json，**不**用 launchd / systemd）
  - install.sh：检测平台 → curl 二进制 → 写入 `/usr/local/bin` 或 `~/.local/bin` → 提示 `agent-meshd start` 即可
  - GitHub Release workflow：tag 触发 → 跨平台编译 → 上传 release artifact
- **Day 4（M2）** 前端迁入 + 内嵌
  - `frontend/` → `meshd/web/`，Vite 配置不变
  - meshd 加资源嵌入：build 时把 `web/dist/*` 编译进二进制，`/` 路径返回 index.html，`/assets/*` 返回静态资源
  - 前端 `api/client.ts` 区分两类 API：
    - **本机 API**（`/api/*`）：实例启停、签 key、本机状态
    - **Gateway API**（meshd 代理转发）：所有原有 admin / mesh API
  - meshd 加代理层：`/api/gateway/*` → 用 user JWT 透传到 Gateway
- **Day 5（M3）** Settings + 实例上下线
  - 新增 `Settings.tsx`：登录态 / meshd 版本 / 升级检查 / 本机日志查看
  - `Agents.tsx` 加"在本机运行"开关：调用 `POST /api/instances/{agent_id}/start` → meshd 自动签 API Key（明文存 keychain，不返给 UI） → 起 worker
  - Gateway 加 `agent_runtime_locks` 表 + API：worker 启动时 INSERT 抢锁，失败说明别处在跑
  - 心跳 60s 超时自动释放锁（用现有定时清理任务扩展）
- **Day 6（M4）** Market 后端 + UI
  - migration 0007：`agent_publications` 表（id, agent_id, publisher_uid, title, summary, system_prompt_template, category, tags, downloads, created_at）+ `agent_subscriptions` 表
  - Gateway 加 admin API：`POST /publications` 发布 / `GET /publications` 列表 / `POST /publications/{id}/fork` 一键 fork
  - 新增 `Market.tsx`：浏览 / 详情 / fork 按钮
  - `Agents.tsx` 加"发布到市场"按钮
- **Day 7** 端到端验证 + 文档
  - 全新机器跑 install.sh → 浏览器登录 → 创建 alice → 在本机运行 → 发任务 → 看到回复
  - 在第二台机器再启动 alice → 报"already running"
  - Market：alice fork 一个 bob 模板 → 一键运行 → alice 和 bob 协作
  - 文档：`docs/quickstart.md` 重写（一行命令安装 → 浏览器开干）；老的 gas-ts README 标 deprecated

**里程碑 9（meshd）**：用户在干净环境一行命令装好 → 浏览器登录 → 创建 agent → 在本机运行 → 看到协作 → 全程不开终端、不写 env、不碰 curl。Market 可用，可 fork。

---

## 6. 里程碑验收标准（汇总）

| Week | 关键验收 |
|---|---|
| 0 | k3d 集群跑 2 副本服务 + 探针生效 + 镜像构建流程通 |
| 1 | curl 完成用户注册/登录/建 agent/prober 探活 |
| 2 | Alice 和 Bob 互加好友全流程 + market 搜索 |
| 3 | 扫表版 Task：1000 task 在 2 副本下 0 重复 0 丢失 + kill Pod 孤儿恢复 |
| 4 | 跨 Pod 点对点 A↔B + 群组 5 人 fan-out，rollout restart 0 丢失 |
| 5 | Grafana + 告警 + HPA + 降级行为符合预期 + 群组 API 可用 |
| 6 | 前端 100% UI 操作闭环（含群聊视图） |
| 7 | Helm 一键部署 + 压测 + chaos + 文档 |
| 8 | 生产 K8s 集群 beta 上线 + 关键 bug 修完 |
| 9 | install.sh 一行装好 → 浏览器一站式 → agent 跑起来 → Market 可用 |

---

## 7. 每周例行

- **周一**：规划 3 个本周最重要的事（写进 TodoWrite）
- **周三**：mid-week checkpoint，看是否偏离
- **周五**：给自己 demo，验证里程碑达成情况
- **周日**：写复盘（docs/weekly/wkN.md），列当周改动、卡点、下周重点

---

## 8. 风险 + 预案

| 风险 | 预案 |
|---|
| K8s 学习曲线拖慢进度 | Week 0 专门留 5 天，不怕慢；用云厂商托管而非自建 |
| Week 3 reliable task 过于复杂 | 允许简化：重试策略、孤儿恢复做最小实现，完善到 Week 7 |
| GAS 迁移 API 改动大 | Week 4 前评估超 3 天就加路径兼容层，GAS 保持不动 |
| 前端不熟悉 | 降级到极简 HTML + fetch，先跑通关键路径 |
| 压测发现容量严重不达标 | Week 7 专门腾时间优化，必要时延 Week 8 |
| 生病 / 中断 | 每日 commit + 每周复盘，保持可接手状态 |
| K8s 生产集群成本 | 内部 beta 可用 staging 级别集群；正式生产再升配 |

---

## 9. 分布式分层升级（Week 10-19）

> 详细设计见 `docs/adr/015-kafka-distributed-architecture.md`

### 目标

单体 Gateway → 分布式分层架构（API Gateway + Identity Svc + Messaging Svc + Push Gateway），Kafka 为核心消息总线。

### 阶段规划

| Phase | Week | 交付 | 核心改动 |
|---|---|---|---|
| **1: Kafka 基础设施** | 10 | Kafka 双写验证通过 | docker-compose 加 Kafka（KRaft）；inbox 双写 |
| **2: Consumer 替代长轮询** | 11-12 | meshd Kafka 消费，延迟 <50ms | meshd 加 kafkajs；保留 HTTP poll fallback |
| **3: Transactional Outbox** | 13-14 | exactly-once 投递 | 激活 outbox_events；dispatcher → Kafka |
| **4: 服务拆分** | 15-17 | 三服务独立部署 | Identity/Messaging/Push 拆 binary；gRPC proto |
| **5: 群组 Kafka 扇出** | 18-19 | 1 次 produce 替代 N 次 INSERT | group.fanout topic；事件溯源 |

### 模块职责

| 模块 | 职责 | 数据归属 | 扩缩 |
|---|---|---|---|
| API Gateway | 路由/鉴权/限流/TLS | 无 | QPS |
| Push Gateway | WebSocket + Kafka consume → 推浏览器 | 无 | 连接数 |
| Identity Svc | 用户/Agent/好友/群组/Market/API Key | users, agents, friendships, groups | QPS |
| Messaging Svc | Task 状态机/消息/Outbox→Kafka/auto-close | tasks, messages, outbox | 消息 QPS |
| meshd | Kafka consume/LLM 推理/worker 管理 | 本地 cursor + session | 每机一个 |

### 通信规则

- 同步（需要响应）→ gRPC：Messaging → Identity 校验权限
- 异步（不需要响应）→ Kafka：Messaging → meshd/Push GW 投递事件
- 缓存（高频读）→ Redis：好友关系、Agent 状态
- 禁止：服务间直连对方 DB

### Topic 设计

| Topic | Key | 用途 | 保留 |
|---|---|---|---|
| `inbox.events` | agent_id | agent 消息投递 | 7d |
| `task.lifecycle` | task_id | task 状态变更 | 3d |
| `group.fanout` | group_id | 群组扇出 | 3d |
| `feed.realtime` | uid | WebSocket 推送 | 1d |

### 里程碑验收标准

| Phase | 验收 |
|---|---|
| 1 | Kafka UI 看到 inbox.events 有消息；现有功能不受影响 |
| 2 | 发消息后 meshd <50ms 收到；kill Kafka 自动 fallback HTTP poll |
| 3 | kill Kafka → outbox 积压 → 恢复后自动追上；事务回滚 → Kafka 无消息 |
| 4 | 三服务独立启停；kill Identity → Messaging 降级到缓存仍可投递 |
| 5 | 群组 10 人发消息 → 全员 <50ms 收到；新成员可回放历史 |

---

## 10. 不在本次范围（后续 Roadmap）

明确标记**不做**，避免 scope creep：

- 多集群 / 联邦 mesh
- 容量感知调度 / 租户公平队列
- 群组进阶：订阅主题（topic 过滤） / 广播权限分层 / 群管理角色
- 跨设备同一 agent **自动**主备切换（V1 走"启动抢锁，第二台报错"，详见 ADR 014）
- 非 Claude framework 的 GAS adapter
- Skill marketplace（评分 / 付费 / 审核） —— Week 9 的 Market 是基础版（发布 / fork），评分付费审核是后续
- 审计 / 计费系统
- 服务网格集成（Istio / Linkerd）
- 自研 K8s Operator
- 任务路径也迁移到 Outbox + MQ（当群组消息路径稳定后，任务可统一走事件流，进一步简化架构）

每一项都列在 `docs/roadmap-v2.md` 待后续规划。

---

## 11. 参考工具栈

| 类别 | 选型 |
|---|---|
| Go 框架 | Hertz |
| ORM | GORM |
| Redis 客户端 | go-redis/v9 |
| 日志 | zap |
| 追踪 | OpenTelemetry + Jaeger / Tempo |
| 指标 | Prometheus + Grafana |
| 限流 | 自研 memory（预留 cluster 扩展）|
| 熔断 | sony/gobreaker |
| 前端 | Vue 3 + Vite + Element Plus |
| 数据库 | MySQL 8 |
| 缓存 / PubSub | Redis 7 |
| K8s 本地 | k3d |
| K8s 生产 | 云厂商托管 EKS / GKE / ACK |
| Helm | v3 |
| CI/CD | GitHub Actions / GitLab CI |
| Secret 管理 | ExternalSecrets + 云厂商 KMS |
| 密钥开发 | .env + dotenv |
| Dockerfile | multi-stage + distroless |

---

## 12. 开干 checklist（Day 0 前）

- [ ] k3d / kubectl / helm / docker 本地就绪
- [ ] 注册云厂商托管 K8s（EKS / GKE / ACK 任选，最小配）
- [ ] GitHub / GitLab 项目 repo 建好
- [ ] CI 账号准备（GitHub Actions 免费额度够）
- [ ] 镜像仓库（Docker Hub 或云厂商 Registry）
- [ ] 阅读本 PLAN 一遍，确认每个 Week 都能交付

全部 ✓ 后从 Week 0 开始。

