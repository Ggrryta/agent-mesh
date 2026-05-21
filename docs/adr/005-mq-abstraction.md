# ADR-002: MQ Abstraction (InMemory MVP, Future Kafka/NATS)

- **Status**: Accepted
- **Date**: 2026-05-12

## Context

Reliable Task 系统需要一个消息总线用于：

- OutboxDispatcher publish TaskEvent
- TaskWorker consume TaskEvent 执行

MVP 阶段只需要单进程 / 多副本共享，未来可能需要跨集群 / 高吞吐。

## Decision

抽象接口 `TaskEventPublisher` + `TaskEventConsumer`，**MVP 用 InMemory 实现**，接口约束"语义上支持多副本消费组"，未来可替换 NATS / Kafka。

```go
type TaskEventPublisher interface {
    Publish(ctx context.Context, event TaskEvent) error
}

type TaskEventConsumer interface {
    // Subscribe 返回事件 channel + unsubscribe 函数
    // 语义：同事件只被消费组内一个消费者收到
    Subscribe(group string) (<-chan TaskEvent, func())
}
```

## Why not directly Kafka/NATS in MVP

- 增加运维复杂度，MVP 不值
- OutboxDispatcher 已经用 DB 层 `SELECT FOR UPDATE SKIP LOCKED` 保证并发安全，MQ 只做"实时触达"
- 即使 MQ 完全丢消息，5s 定时扫表兜底

## Why abstract instead of直接进程内 channel

- Worker 不能耦合 InMemory 实现细节
- 未来换 Kafka 时：只改 `infra/mq/` 的注入，domain 代码不动
- 接口强制约定"消费组"语义，InMemory 实现也要支持

## InMemory 约束

- 进程内 `chan TaskEvent`，buffered
- 多副本时每个 Pod 内独立 channel（靠 OutboxDispatcher 的 DB 并发安全保证全局不重复）
- ErrTaskQueueFull 时 OutboxDispatcher 重试

## 迁移路径

未来换 Kafka：
1. 新增 `infra/mq/kafka.go` 实现两个接口
2. 改 `main.go` 注入
3. Consumer group 用 `agent-mesh-worker`
4. domain 代码零改动

未来换 NATS：同样模式。
