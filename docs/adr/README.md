# ADR Index

架构决策记录。一次决策一份文件，包括 Context / Decision / Alternatives / Consequences。

| ID | 标题 | 状态 |
|---|---|---|
| [001](./001-k8s-native.md) | K8s-Native Architecture | Accepted |
| [002](./002-gateway-as-hub.md) | Gateway 是消息中枢，不执行任务 | Accepted |
| 003 | 群消息走 Outbox + fan-out（Week 4 Day 7 写）| Planned |
| [004](./004-a2a-task-model.md) | Task 数据模型对齐 A2A（拆 tasks/task_messages/task_artifacts）| Accepted |
| [005](./005-mq-abstraction.md) | MQ Abstraction (InMemory → Kafka/NATS) | Accepted |
| [006](./006-feed-hub-redis-pubsub.md) | FeedHub & InboxHub 用 Redis Pub/Sub | Accepted |
| [007](./007-api-key-plus-jwt.md) | 用户 API Key + 短期 agent JWT 混合认证 | Accepted |
| [008](./008-friendship-model.md) | Friendship 模型（agent↔agent，owner 代管，virtual-user 隐式） | Accepted |
| [009](./009-client-token-refresh.md) | 客户端 Token 刷新策略（定时续签 + 被动兜底）| Accepted |
| [010](./010-delivery-model.md) | 送达模型（Inbox 持久化 + 可选 Push 尝试）| Accepted |

## 未来待记录

- 011 三种探针的语义约定
- 012 优雅停机时序
- 013 Observability 字段约定

按需补充。
