# ADR 015: 引入 Kafka 的分布式分层升级

**Status**: Proposed
**Date**: 2026-05-15

## Context

当前 agent-mesh 是单体 Gateway + MySQL inbox 表做消息队列。随着 agent 规模增长，暴露以下瓶颈：

1. **写入和投递耦合**：inbox INSERT 必须跟业务写入同事务，否则消息丢失
2. **长轮询占连接**：每个 agent 常驻一个 HTTP 连接等事件
3. **扇出成本高**：群组 N 人 = N 次 DB INSERT
4. **无法独立扩缩**：消息层和身份层绑在同一个进程

## Decision

引入 Kafka 作为核心消息总线，分 5 个阶段渐进式升级为分布式分层架构。每个阶段独立可交付、可回滚。

### 目标架构

```
┌─────────────────────────────────────────────────────┐
│                    接入层                             │
│  ┌──────────────┐         ┌───────────────┐         │
│  │ API Gateway   │         │ Push Gateway   │         │
│  │ (路由/鉴权/限流) │         │ (WebSocket)    │         │
│  └──────┬───────┘         └───────┬───────┘         │
└─────────┼─────────────────────────┼─────────────────┘
          │ gRPC                    │ Kafka consume
┌─────────┼─────────────────────────┼─────────────────┐
│         │        业务层           │                   │
│  ┌──────▼──────┐   ┌─────────────▼──────┐           │
│  │ Identity Svc │   │  Messaging Svc      │           │
│  │ users/agents │   │  task + outbox      │           │
│  │ friends/groups│   │  produce → Kafka    │           │
│  └─────────────┘   └──────────┬──────────┘           │
└────────────────────────────────┼─────────────────────┘
                                 │ produce
┌────────────────────────────────┼─────────────────────┐
│                    消息层       │                      │
│              ┌─────────────────▼──────┐               │
│              │         Kafka           │               │
│              │  inbox.events           │               │
│              │  task.lifecycle          │               │
│              │  group.fanout           │               │
│              │  feed.realtime          │               │
│              └────────────────────────┘               │
└──────────────────────────────────────────────────────┘
```

### 模块职责

| 模块 | 职责 | 不做 | 扩缩依据 |
|---|---|---|---|
| **API Gateway** | 路由转发、JWT 验证、全局限流、TLS、日志 | 业务逻辑、DB 读写 | QPS |
| **Push Gateway** | WebSocket 管理、Kafka consume → 推浏览器 | 业务逻辑、持久化 | 连接数 |
| **Identity Svc** | 用户/Agent/好友/群组/Market/API Key | Task、消息投递 | QPS（读多写少） |
| **Messaging Svc** | Task 状态机、消息追加、Outbox→Kafka、auto-close | 用户管理、WebSocket | 消息 QPS |
| **meshd** | Kafka consume、LLM 推理、worker 管理、Fan-out collector | 持久化、权限校验 | 每机一个 |

### 服务间通信规则

- **同步调用**（需要响应）→ gRPC：Messaging → Identity 校验权限；meshd → Messaging 发消息
- **异步通知**（不需要响应）→ Kafka：Messaging → meshd/Push GW 投递事件
- **缓存查询**（高频读）→ Redis：好友关系、Agent 状态、群组成员
- **禁止**：服务间直连对方的 DB

### 数据归属

- **Identity DB**：users, agents, skills, api_keys, friendships, groups, group_members, agent_publications
- **Messaging DB**：reliable_async_tasks, task_messages, task_artifacts, outbox_events
- **Kafka**：inbox.events / task.lifecycle / group.fanout / feed.realtime
- **Redis**：identity:* 缓存 / session:* 黑名单 / feed:* 连接状态

### Topic 设计

| Topic | Key | 用途 | 保留期 |
|---|---|---|---|
| `inbox.events` | agent_id | agent 消息投递 | 7 天 |
| `task.lifecycle` | task_id | task 状态变更通知 | 3 天 |
| `group.fanout` | group_id | 群组 timeline 扇出 | 3 天 |
| `feed.realtime` | uid | WebSocket 实时推送 | 1 天 |

### 降级策略

| 故障 | 降级 |
|---|---|
| Identity 不可用 | Redis 缓存放行（30s TTL） |
| Kafka 不可用 | Outbox 积压等恢复；meshd fallback HTTP 长轮询 |
| Push GW 不可用 | WebSocket 断开，消息不丢（agent 从 Kafka 消费） |
| Redis 不可用 | 直调 gRPC（延迟增加但功能正常） |

## Implementation Phases

| Phase | 时间 | 目标 | 可回滚 |
|---|---|---|---|
| 1 | Week 1 | Kafka 基础设施 + 双写（inbox 表 + Kafka） | ✅ 删 produce 调用 |
| 2 | Week 2-3 | meshd Kafka consumer 替代长轮询 | ✅ 不设 KAFKA_BROKERS 走旧路径 |
| 3 | Week 4-5 | Transactional Outbox 保证 exactly-once | ✅ 停 dispatcher + 恢复直写 |
| 4 | Week 6-8 | 三服务拆分 + gRPC 通信 | ✅ 合回单体 |
| 5 | Week 9-10 | 群组 Kafka 扇出 + 事件溯源 | ✅ 回退到 DB fan-out |

## Technology Choices

| 组件 | 选择 | 理由 |
|---|---|---|
| Kafka 客户端（Go） | `github.com/segmentio/kafka-go` | 纯 Go，无 CGO |
| Kafka 客户端（TS） | `kafkajs` | Node.js 生态最成熟 |
| Kafka 部署 | KRaft 模式 | 无 ZooKeeper，简化运维 |
| 序列化 | JSON → Avro（Phase 4+） | 先快后稳 |
| 服务间通信 | gRPC + protobuf | 强类型 + 代码生成 |

## Consequences

### 正面
- 写入和投递彻底解耦（Outbox 保证原子性）
- 消息延迟从 500ms 降到 ~10ms
- 群组扇出从 N 次 INSERT 变成 1 次 produce
- 各服务独立扩缩、独立部署、独立迭代
- 事件溯源能力（Kafka 保留历史消息）

### 负面
- 运维复杂度增加（Kafka 集群 + 多服务）
- 最终一致性（Outbox 有 100ms 延迟）
- 消息可能重复（需要 consumer 端幂等）
- 调试链路变长（跨服务 tracing 必须完善）

## References

- ADR 002: Task 域设计
- ADR 005: MQ 抽象层（预留接口）
- ADR 010: Inbox 投递模型
- ADR 014: meshd 本机服务
