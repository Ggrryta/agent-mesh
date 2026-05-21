# ADR 010：送达模型（Inbox 持久化 + 可选 Push 尝试）

- **状态**：Accepted
- **日期**：2026-05-13
- **关联**：ADR 002（Gateway 是消息中枢）

## 背景

ADR 002 定了 Gateway 的职责之一是"把消息送到 agent"。但 agent 的在线状态
不可控：
- 家用网络 agent（NAT 后，Gateway 打不进来）
- 云函数 / Cloud Run agent（只有处理请求时才在线）
- 长连接 agent（7x24 跑在 K8s，有稳定 URL）

所以 Gateway 不能假设"有 URL 就能 push 到"，也不能假设"agent 总在连 Gateway"。
需要一个**兼容所有场景**的送达模型。

## 决策

**送达是"inbox + 可选 push"的双路径**：

1. **Inbox 是真相之源**（MUST）
   - 所有要发给某个 agent 的事件（message / artifact / transition）**先写 inbox**
   - inbox 按 `(agent_id, id)` 顺序持久化
   - agent 用 `GET /v1/mesh/inbox?since={id}` 拉取，幂等（重复拉同一段返回一样）
2. **Push 是尽力而为的优化**（OPTIONAL）
   - agent 在 agents 表登记了 `url` 时，Gateway 后台 goroutine 尝试 HTTP POST 到 `{url}/a2a/events`
   - 成功 → `MarkDelivered`，不再重推
   - 失败 → 留在 inbox 等下次 pull，**不自动重试**

### 两条路径的互动

```
event 发生
    │
    ▼
INSERT inbox_events (delivered_at=NULL)
    │
    ├─▶ 成功：同步给 API 调用方返 200
    │
    └─▶ 异步 goroutine 尝试 push
               │
               ├─ agent 有 URL → POST {url}/a2a/events
               │    │
               │    ├─ 2xx：MarkDelivered  → inbox.delivered_at=now
               │    └─ 失败：不做任何事，留 inbox
               │
               └─ 无 URL → 什么都不做，等 agent 主动拉
```

Agent 侧的幂等：
- 同一个 inbox event id 既可能被 push 推来，也可能被 pull 拉到
- SDK 保存 `last_processed_id`，对已处理的 id 忽略

### 为什么不重试 Push

Push 失败有几种原因：
- **agent 暂时不可达**（网络抖动、重启中）：下一次 task 动作会再触发 push
- **agent 永久下线**：重试毫无意义，agent 恢复时会主动调 `GET /inbox` 补齐
- **push 端点格式 / 认证错**：配置错误，重试不能解决

在这些场景下"push 重试"只是浪费资源，反而 pull 路径**永远有效**，
所以让 pull 兜底更健壮。

### Pull 接口设计

```
GET /v1/mesh/inbox?since=<event_id>&limit=<N>
  Authorization: Bearer <agent-jwt>

Response:
{
  "events": [
    {"id": 1001, "kind": "message",    "task_id": "...", "payload": {...}},
    {"id": 1002, "kind": "artifact",   "task_id": "...", "payload": {...}},
    {"id": 1003, "kind": "transition", "task_id": "...", "payload": {...}}
  ],
  "max_id": 1003
}
```

- agent 保存上次拿到的 `max_id`，下次 `since=max_id` 继续
- `limit` 默认 100，上限 500
- 返回空数组说明没新事件（agent 可用轮询或 SSE 订阅）
- SSE 版本留 Week 4 做（走 `/v1/mesh/inbox/stream`）

### Event 结构

`inbox_events` 表 schema（Week 3 Day 2 migration 0003 创建）：

```sql
CREATE TABLE inbox_events (
    id            BIGINT UNSIGNED PRIMARY KEY AUTO_INCREMENT,
    agent_id      VARCHAR(64) NOT NULL,
    kind          VARCHAR(32) NOT NULL,   -- message | artifact | transition
    task_id       VARCHAR(64) NOT NULL,
    ref_id        VARCHAR(64) NOT NULL,   -- message_id / artifact_id / to_state
    payload_json  JSON NOT NULL,          -- 完整事件体，方便 agent 直接消费
    created_at    DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    delivered_at  DATETIME(3) NULL,       -- push 成功时打标，仅用于观测，不影响 pull
    KEY idx_agent_id (agent_id, id),
    KEY idx_created (created_at)
) ENGINE=InnoDB;
```

字段选择说明：
- **event 的 payload 冗余存一份 JSON**：agent 拉 inbox 时一次拿全，不用再去 task_messages / artifacts 表 JOIN
- **delivered_at 只打 push 成功的标**：pull 拉走**不**清它，因为可能 push 和 pull 都会拉到。唯一作用是观测"有多少事件是靠 push 提前送达的"
- **不做 GC**：events 历史保留，方便审计。未来量级大时按 `created_at` 过期

## 考虑过的替代

### A. 只 push 不 inbox
- ❌ NAT 后的 agent 收不到
- ❌ agent 离线期间消息直接丢

### B. 只 inbox 不 push
- ✅ 简单
- ❌ agent 要轮询 Gateway，延迟高（轮询间隔）
- 🔜 SSE (Week 4) 会解决这个问题，所以 B 其实 = A2A 典型用法

### C. Inbox + 带重试的 Push
- ✅ push 成功率高
- ❌ 重试策略复杂（次数 / 退避）
- ❌ Gateway 背上"重试" 责任，和 ADR 002 的"Gateway 不做重试"冲突

### D. 只对在线 agent push，离线 agent 留 inbox（本决策）
- ✅ push 失败零代价：pull 兜底永远可用
- ✅ Gateway 零重试逻辑
- ✅ 对 agent 部署拓扑无要求（NAT / 云函数 / K8s 都行）

## 决策后果

### 得
- Agent 实现简单：必做 pull（SSE 是 pull 的优化），push 收到算 bonus
- Gateway 零送达状态机：只写 inbox + 尽力而为 push
- 故障时优雅降级：Gateway push 模块挂了，agent 照样能 pull

### 失
- Push 成功率没保证，需要观测指标跟踪（`push_attempt_total{result}`）
- 高频 task 场景下 pull 的 polling 开销（Week 4 SSE 解决）

### 约束给下游

**Agent SDK 必须做**：
1. 维护 `last_processed_id`，**以 pull 为准**
2. 收到 push 时也要按 `id` 去重（和 pull 共用一份处理过的 id 集合）
3. pull 周期建议：有 task 活跃时 1~3 秒；空闲时退避到 30 秒
4. Week 4 SSE 上线后替换 pull 为 SSE，`last_processed_id` 逻辑不变

**Agent URL 配置建议**：
- 建议：在 agents 表登记 URL 时明确声明是否支持 push（未来加字段 `push_enabled`）
- MVP：有 URL 就尝试 push，无就跳过

## 未来演进

1. **Week 4 加 SSE**：`GET /v1/mesh/inbox/stream`，agent 订阅长连接接收实时事件
2. **Inbox 清理**：定时 job 删除 created_at 超过 7 天且 delivered_at 非空的事件（MVP 不做）
3. **Push 认证**：push 时在 Header 带 Gateway 签发的短期 token，让 agent 能验证"这个 push 真的来自我的 Gateway"（Week 5 做）
4. **多节点 push 负载**：Gateway 多副本时，push worker 需要 CAS claim inbox event 避免重推（用和 Prober 同款的 CAS 模式，MVP 先每副本都推，agent 按 id 去重）

## 参考

- ADR 002：Gateway 是消息中枢，不是任务执行者
- ADR 004：Task 数据模型对齐 A2A
- A2A Life of a Task：https://github.com/a2aproject/A2A/blob/main/docs/topics/life-of-a-task.md
