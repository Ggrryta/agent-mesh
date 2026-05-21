# ADR 012：FeedHub 设计（前端实时事件推送）

- **状态**：Accepted
- **日期**：2026-05-13
- **关联**：ADR 011（Long-poll 替代 SSE）、DESIGN.md §11.5

## 背景

用户需要在前端控制台实时看到 agent 之间的交流过程。DESIGN.md 定义了
`/admin/ws/feed` WebSocket 端点 + FeedHub 跨 Pod 广播机制。

## 决策

### 架构

```
Mesh API handler（task 写入成功后）
  → publishFeed(ownerUID, event)
  → Redis PUBLISH feed:user:{uid} {event_json}
  → 所有 Gateway Pod 的 FeedHub.Run() 收到
  → 查本地 subscriber map
  → 有 WebSocket 连接 → 推送
  → 无连接 → 丢弃（前端不在线不存）
```

### WebSocket 协议

- 端点：`GET /v1/admin/ws/feed`（RequireUser 中间件）
- 方向：服务端 → 客户端（只读连接）
- 保活：30s ping
- 事件格式：
  ```json
  {
    "type": "task_message|task_artifact|task_transition|task_created",
    "agent_id": "alice",
    "task_id": "t-xxx",
    "payload": {...},
    "timestamp": "2026-05-13T10:00:00Z"
  }
  ```

### 触发点

在 mesh handler 层（handleSubmitTask / handleAppendMessage / handleAppendArtifact /
handleTransition）成功返回前调用 `publishFeed`。只推已确认持久化的事件。

### 用户隔离

通过 `agentLookup.Lookup(agentID)` 查 ownerUID，只推给 agent 的 owner。
同一用户名下的多个 agent 的事件都推到同一个 WebSocket 连接。

## 理由

- Redis Pub/Sub 是 DESIGN.md 已定的跨 Pod 方案，零额外基础设施
- WebSocket 比 SSE 更适合前端（双向、浏览器原生支持好）
- FeedHub 与 InboxHub 解耦：前端实时性和 agent 消息投递是独立关注点
- 前端断线重连后用 REST API 补齐历史（`GET /admin/tasks?agent=xxx`）

## 后果

- 新增 `gorilla/websocket` 依赖
- Redis Pub/Sub 用于 FeedHub（`feed:user:*` pattern subscribe）
- 前端不在线时事件丢弃，不持久化（历史通过 REST 拉取）
