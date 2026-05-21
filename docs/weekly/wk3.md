# Week 3 — Reliable Task（消息中枢版，A2A 对齐）

> 2026-05-13。Week 3 主体完成。重大职责修正：**Gateway 不执行任务**。

## 核心决策修正

Week 3 开工前发生了**根本性的职责重定义**。对齐 A2A 协议后发现原 PLAN
里"Gateway 跑 Worker 执行 task"的设计错位：

- Task 是 A2A 协议里的**业务实体**（有 history / artifacts / contextId），
  不是"后台作业"
- 执行方是 **被叫 agent**（Bob）自己，有自己的业务逻辑、checkpoint、
  crash 恢复；Gateway 不该代劳
- input-required 语义要求任务中途挂起等 user，这是 workflow 级能力，
  Worker 模型做不到

**新定位**（详见 ADR 002）：
- Gateway = **消息中枢**，做四件事：持久化 Task 事实 / 路由消息与产物 /
  送达保证 / 最小语义校验
- Gateway **不**调 agent、**不**做重试、**不**跑 Worker、**不**做孤儿回滚

相应地 PLAN.md Week 3 章节被重写；ADR 002 + ADR 010 作为开工前置决策落地。

## 已交付

### 决策与文档
- **ADR 002**：Gateway 是消息中枢，不执行任务
- **ADR 010**：送达模型（Inbox + 可选 Push）
- **ADR 004**：补充 Week 3 实现细节（保留字段 / role 推断 / JSON tag / 状态机）
- **PLAN.md** Week 3 章节重写
- **docs/api.md** Task / Inbox 章节重写

### 代码（按 Day）

**Day 1：task 域**
- `domain/task/model.go`：9 个 State、Role、PartKind、状态机转换矩阵 `allowedTransitions`
- `domain/task/repo.go`：CreateTask（事务）/ AppendMessage（幂等）/ AppendArtifact /
  TransitionStatus（CAS）/ GetTask / ListByContext / GetMessageByID
- `domain/task/service.go`：Submit / AppendMessage / AppendArtifact /
  Transition / Get / ListByContext，授权 + 状态机校验
- 单测 + live 集成测试（覆盖 CAS 并发、事务幂等、跨 task message_id 冲突）

**Day 2：inbox + push delivery**
- migration 0003：`inbox_events` 表
- `domain/inbox/`：Event / Kind / TransitionPayload + Service（EnqueueMessage /
  EnqueueArtifact / EnqueueTransition / Pull / MarkDelivered），实现 `task.Inboxer` 接口
- `internal/delivery/push.go`：Pusher worker + AgentURLLookup 接口
- `domain/agent/lookup.go`：补 `LookupURL` 方法
- 单测 + live + httptest push 场景

**Day 3：Mesh API 7 个端点**
- POST /v1/mesh/tasks
- GET /v1/mesh/tasks/{id}?include=history,artifacts
- GET /v1/mesh/tasks?context_id=xxx
- POST /v1/mesh/tasks/{id}/messages
- POST /v1/mesh/tasks/{id}/artifacts
- POST /v1/mesh/tasks/{id}/transition
- GET /v1/mesh/inbox?since=X&limit=N
- 错误映射：task 域错误 → HTTP code（404 / 403 / 409 / 400）

**Day 4：装配 + E2E**
- `cmd/server/main.go` 接 task / inbox / pusher / agentLookup
- inbox.Service 加 WithNotifier 钩子，Enqueue 后通知 pusher
- E2E 2 副本 K8s 集群全通

**Day 5：ADR + docs + weekly**（本文档）

### E2E 验证（K8s 双副本）

完整场景（Alice → Bob）：
1. ✅ Alice POST /tasks → Gateway 持久化 + 路由 Bob inbox
2. ✅ Bob GET /inbox → 收到 message 事件（payload 是 A2A-shaped Message）
3. ✅ Bob POST /transition(working) → Alice inbox 收到 transition
4. ✅ Bob POST /artifacts → Alice inbox 收到 artifact
5. ✅ Bob POST /transition(completed) → Alice inbox 收到终态

input-required 多轮：
6. ✅ Bob → working → input-required；Alice 追加 message + 转回 submitted；
   Bob 继续 working → completed
7. ✅ history 顺序正确（user / agent / user）

约束验证：
- ✅ 非好友拒绝（40001 "agents are not friends"）
- ✅ to=virtual-user 拒绝（防"任务反抛给用户"）
- ✅ 终态 Task 拒绝追加 message
- ✅ completed → working 非法转换拒绝（40900 invalid_transition）
- ✅ context_id 聚合正确
- ✅ cancel 任意阶段（submitted → canceled 合法）

## 指标汇总

| 包 | 覆盖率 | 备注 |
|---|---|---|
| pkg/auth | 92.3% | |
| internal/middleware | 79.4% | |
| internal/api/admin | 75.3% | |
| internal/api/mesh | **25.7%** | 新加 7 个 task 端点降低；handler 层的主要路径在 E2E 覆盖 |
| internal/delivery | **89.1%** | Week 3 新增 |
| internal/domain/task | **77.1%** | Week 3 新增 |
| internal/domain/inbox | **77.9%** | Week 3 新增 |
| internal/domain/agent | 67.5% | 略降（新加 LookupURL 未补测） |
| internal/domain/apikey | 86.6% | |
| internal/domain/friendship | 80.9% | |
| internal/domain/prober | 90.2% | |
| internal/domain/user | 80.3% | |

代码总量新增约 **1800 行**（生产 1200 + 测试 600），匹配开工时估计。

## 关键工程决策（供后续 week 参考）

1. **AgentLookup 复用**：`agent.NewLookupAdapter` 同时服务 friendship 和
   task；push worker 单独用 LookupURL。跨域 agent 查询零散落。

2. **inbox.Service.WithNotifier 钩子**：task.Service Enqueue 成功后，
   inbox 自身通知下游 pusher。task 层对"有没有 push"完全无感知，
   测试 / mock 时直接不接 notifier 即可。

3. **Message.role 由 Gateway 推断**：不接受 caller 传入 role，防止
   serving agent 伪造 user 回复。实现在 `service.resolveRole`。

4. **状态机 allowedTransitions 独立表**：不写在函数里，独立常量 map，
   便于单测矩阵驱动。`StatesAllowingTransitionTo(to)` 反查 from 集合，
   直接喂 SQL CAS。

5. **task JSON tag 对齐 A2A**：model.go 里 Message / Artifact / Task 的
   JSON tag 都是 snake_case 对齐协议。inbox payload 直接 marshal 这些
   结构体，agent SDK 反序列化就是 A2A 对象。

6. **CreateTask 用事务**：主表 INSERT + 首条 message INSERT 放一个 tx，
   避免"task 建了但首条消息没落盘"的半态。两边都用 `isDup` 兜底重试。

7. **TransitionStatus 返回最新状态**：无论 CAS 是否成功，都读回一次返回
   给调用方。`changed=false + 最新 task`，让业务逻辑能区分"是我抢到了"
   vs"别人改走了，现在是什么状态"。

## 遗留 / 后续

### 没做的（刻意留给未来）

- **Prometheus 指标**：原 PLAN 要求 8 个 counter，Week 3 没上。Week 5
  observability 章节集中补齐（统一一次性接 OTel + Prometheus）
- **OTel trace**：同上
- **SSE 订阅 inbox**：Week 4 做
- **inbox GC**：MVP 不清理，任由 inbox_events 表涨
- **Push 多副本 claim**：现在多副本各自推一次，agent 靠 id 去重。量大
  了再加 CAS claim

### 略微不完美的点

- mesh handler 测试覆盖率 25.7% 过低 —— 新增 7 个端点主要靠 E2E 验证，
  unit test 没补。Week 5 / Week 6 集中补一次
- agent.LookupURL 新加没单测
- Day 5 的 PLAN 要求里的 Prometheus counter 推迟
- docs/api.md §Task 章节里 status transition 图是 ASCII，可读性一般

这些都记在此 weekly，后续 Week 按需补。

## 下一步（Week 4）

原 PLAN Week 4 是 **Inbox Hub + SSE + OutboxDispatcher + 群组底座**。按
现在的进度：
- Inbox 基础版 Week 3 做完了（pull + push）
- Week 4 主要补：SSE 订阅 + 群组（Outbox + fan-out）+ MQ 抽象

具体任务 Week 4 开工前再对齐。建议顺序：
1. Day 1-2：Inbox SSE（`GET /v1/mesh/inbox/stream`）
2. Day 3-4：Outbox + MQ 抽象 + InMemory 实现
3. Day 5：群表 + 群消息 fan-out
4. Day 6-7：缓冲 + GAS SDK 初步（按 ADR 009 实现 refresh loop）
