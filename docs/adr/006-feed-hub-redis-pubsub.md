# ADR-003: FeedHub & InboxHub 用 Redis Pub/Sub（多副本原生）

- **Status**: Accepted
- **Date**: 2026-05-12

## Context

两个组件都涉及"跨 Pod 把消息推到建立着长连接的 Pod"：

- **InboxHub**：Task Worker 可能在 Pod A，但目标 agent 的 SSE 连接在 Pod B
- **FeedHub**：任何 Pod 上的业务事件（task.updated / agent.status / friendship.updated）要推给该用户在任意 Pod 上建立的 WebSocket

单实例可以用进程内 Map，但 K8s 多副本就失效。

## Decision

**两个组件直接用 Redis Pub/Sub 实现**，MVP 就支持多副本。不做 InMemory-only 版本。

### InboxHub 模型

```
Publish(agentID, msg):
  rdb.Publish("inbox:agent:"+agentID, msg)

Every Pod subscribes "inbox:agent:*" (keyspace pattern):
  收到 msg → 查本地 Map 有无 agent 的 SSE 订阅者
  有 → 推送；无 → drop（其他 Pod 会处理）
```

### FeedHub 模型

```
BroadcastToUser(uid, event):
  rdb.Publish("feed:user:"+uid, event)

Every Pod subscribes "feed:user:*":
  收到 event → 查本地 Map 有无该 uid 的 WebSocket 连接
  有 → 推送；无 → drop
```

## Why 不用 InMemory-only 版本

单实例 InMemory 代码和多实例 Redis 代码是两套不同的抽象，写两份没价值。多实例是 K8s 默认模式，MVP 就用正解。

## Why 不做消息持久化

Pub/Sub 是 fire-and-forget：

- **InboxHub**：消息已经在 MySQL task 表持久化，Pub/Sub 只是实时触达。推失败 Task 状态不变，Worker 超时后重试。
- **FeedHub**：纯通知（task.updated 等），丢了对业务无影响（前端需要时自己查 API）。

## Why 不用 Streams（Redis 5.0+ 的 XADD/XREAD）

Streams 有持久化 + 消费组，但：

- 成本更高（每条消息都落 Stream）
- 持久化的职责应该在 MySQL 而不是 Redis
- MVP 不需要消息重播能力
- Pub/Sub 简单足够

未来若需要"多消费者独立 offset"（比如审计系统订阅所有 feed），再评估迁移到 Streams。

## 约束

- Redis 不可用时，FeedHub 降级为 noop（不推通知，但不影响核心链路）
- InboxHub 推失败不算 Worker Complete，走 Task 重试
- 连接本身无状态：Pod 崩溃 → 前端 / GAS 重连到新 Pod，从 JWT / DB 恢复上下文，不依赖连接内存

## 代码组织

```
domain/inbox/
  hub.go             # 接口
  distributed.go     # Redis Pub/Sub 实现
  memory.go          # 测试替身（单 Pod 测试用）

domain/feed/
  hub.go
  distributed.go
  memory.go
```

生产始终用 distributed 版本，memory 版本只用于单元测试。
