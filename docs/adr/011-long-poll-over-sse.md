# ADR 011：Long-poll 替代 SSE（agent 侧消息投递）

- **状态**：Accepted
- **日期**：2026-05-13
- **关联**：ADR 010（送达模型）、ADR 002（Gateway 是消息中枢）

## 背景

ADR 010 定义了 inbox + 可选 push 的送达模型。DESIGN.md 和 PLAN.md 原版设计
中，GAS daemon 通过 SSE 长连接（`GET /mesh/inbox/:agent/sse`）实时接收事件。

Week 4 开工前重新审视了 SSE 的必要性。

## 决策

**砍掉 agent 侧 SSE，用 long-poll 替代。**

现有 `GET /v1/mesh/inbox` 端点新增 `wait` 参数（0-30 秒）：
- `wait=0`：立即返回（向后兼容，等价于原有行为）
- `wait=N`：服务端 hold 住请求，有新事件立即返回，超时返回空

GAS daemon 内部循环调用 `PollInbox(since, wait=30)`，实现近实时接收。

## 理由

### 为什么 SSE 不值得

| 维度 | SSE | Long-poll |
|------|-----|-----------|
| Gateway 有状态 | 是（每 agent 一条持久连接） | 否 |
| 跨 Pod 路由 | 需要 Redis Pub/Sub InboxHub | 不需要 |
| 连接管理 | 心跳/断线检测/重连/drain | 无 |
| 延迟 | ~0ms | ~0ms（有数据时立即返回） |
| 实现复杂度 | 高 | 低（复用已有 Pull + ticker） |

### 为什么 long-poll 足够

Agent 间通信是**异步长任务**语义（分钟/小时级）。Bob 处理一个 task 可能要
几分钟，Alice 等回复晚 500ms 收到 vs 晚 0ms 收到，体验无差别。

### 前端实时性怎么保证

前端用户看 agent 交流过程的实时性由 **FeedHub WebSocket**（`/admin/ws/feed`）
保证，走 Redis Pub/Sub 跨 Pod 广播。这是独立通道，不依赖 agent 侧的 SSE。

## 实现

- `inbox.Service.PollWithWait(ctx, agentID, sinceID, limit, timeout)`
- 内部每 500ms 轮询 DB，有数据或超时即返回
- 未来可加 notifier channel 唤醒优化，但 MVP 不做

## 后果

- Gateway 保持完全无状态（Pod 随时可 kill/替换）
- 不需要 InboxHub（Redis Pub/Sub 只用于 FeedHub）
- GAS daemon 实现简化（HTTP 客户端 + 循环，无 SSE 客户端）
- 如果未来有常驻进程类 agent 需要毫秒级推送，可以加回 SSE 作为可选通道
