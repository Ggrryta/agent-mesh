---
title: "消息底座：如何为 AI Agent 构建可靠的分布式通信系统"
date: 2026-05-19
draft: false
categories: ["架构设计"]
tags: ["kafka", "outbox", "消息队列", "幂等", "分布式"]
series: ["架构深度"]
summary: "详解 Agent Mesh 的消息底座——Kafka + Transactional Outbox 如何实现消息不丢失、不重复、有序投递，以及各种故障场景的处理。"
---

> 本文详细介绍 Agent Mesh 项目的消息底座设计——如何让多个 AI Agent 通过 Kafka + Transactional Outbox 实现可靠、有序、不丢失的异步通信。

## 1. 问题背景

多 Agent 协作系统的核心挑战不是 LLM 推理，而是**消息投递**。当 Alice Agent 想问 Bob Agent 一个问题时：

- 消息不能丢（Bob 必须收到）
- 消息不能重复（Bob 不能对同一个问题回答两次）
- 消息要有序（Alice 发的 M1、M2，Bob 必须按顺序收到）
- 不能阻塞（Alice 发完消息后不能干等 Bob 回复，要能继续做别的事）
- 要能扛住积压（Bob 在做长时间推理时，新消息不能丢）

传统的 HTTP 请求-响应模型无法满足这些需求——Agent 的推理时间是 10-30 秒，不能用同步调用。

## 2. 整体架构

```
┌─────────────────────────────────────────────────────────────────────────┐
│                           完整消息链路                                    │
│                                                                         │
│  Alice (meshd)          API Gateway         Messaging Svc               │
│  ┌──────────┐          ┌──────────┐        ┌──────────────┐            │
│  │ LLM 推理  │─ HTTP ──▶│  :8080   │──路由──▶│    :8082     │            │
│  │           │          │ 限流/鉴权 │        │              │            │
│  │ mesh_send │          └──────────┘        │  ┌────────┐  │            │
│  │ _message  │                              │  │ BEGIN  │  │            │
│  └──────────┘                               │  │  TX    │  │            │
│                                             │  ├────────┤  │            │
│                                             │  │INSERT  │  │            │
│                                             │  │ task   │  │            │
│                                             │  │messages│  │            │
│                                             │  ├────────┤  │            │
│                                             │  │INSERT  │  │            │
│                                             │  │outbox  │  │            │
│                                             │  │events  │  │            │
│                                             │  ├────────┤  │            │
│                                             │  │COMMIT  │  │            │
│                                             │  └────────┘  │            │
│                                             │       │      │            │
│                                             │       ▼      │            │
│                                             │  ┌────────┐  │            │
│                                             │  │Outbox  │  │            │
│                                             │  │Dispatch│  │            │
│                                             │  │(1s轮询)│  │            │
│                                             │  └───┬────┘  │            │
│                                             └──────┼───────┘            │
│                                                    │                    │
│                                                    ▼                    │
│                                             ┌──────────────┐            │
│                                             │    Kafka      │            │
│                                             │              │            │
│                                             │ topic:       │            │
│                                             │ inbox.events │            │
│                                             │              │            │
│                                             │ key=bob      │            │
│                                             │ partition 3  │            │
│                                             └──────┬───────┘            │
│                                                    │                    │
│                                                    ▼                    │
│  Bob (meshd)                                                            │
│  ┌──────────────────────────────────┐                                   │
│  │ Kafka Consumer                    │                                   │
│  │ groupId=meshd-bob-coder@example       │                                   │
│  │                                   │                                   │
│  │  ┌─────────┐  ┌──────┐  ┌─────┐ │                                   │
│  │  │ Dedup   │─▶│Handle│─▶│ LLM │ │                                   │
│  │  │ Check   │  │Event │  │推理  │ │                                   │
│  │  └─────────┘  └──────┘  └──┬──┘ │                                   │
│  │                             │     │                                   │
│  │                        mesh_reply │                                   │
│  │                             │     │                                   │
│  └─────────────────────────────┼─────┘                                   │
│                                │                                         │
│                                ▼                                         │
│                    (同样的链路回到 Alice)                                  │
└─────────────────────────────────────────────────────────────────────────┘
```

## 3. 核心设计：Transactional Outbox

### 为什么不直接写 Kafka？

```
// ❌ 错误做法：DB 写入和 Kafka 发送不原子
func AppendMessage(ctx, msg) {
    db.Insert(msg)           // ← 成功
    kafka.Produce(msg)       // ← 如果这里失败，消息写入了 DB 但没发 Kafka
                             //    接收方永远不知道有新消息
}
```

DB 事务和 Kafka produce 是两个独立系统，无法用一个事务包裹。

### Outbox 模式解决原子性

```
// ✅ 正确做法：业务数据和投递指令在同一个事务
func AppendMessage(ctx, msg) {
    tx := db.Begin()
    tx.Insert("task_messages", msg)              // 业务数据
    tx.Insert("outbox_events", {                 // 投递指令
        event_type: "inbox.message:bob",
        payload: serialize(msg),
    })
    tx.Commit()  // 原子：要么都成功，要么都失败
}

// 后台 Dispatcher（独立 goroutine）
func Dispatcher.Run() {
    every 1 second:
        events = SELECT ... FROM outbox_events
                 WHERE status='pending'
                 FOR UPDATE SKIP LOCKED  // 多实例并行安全
                 LIMIT 50
        
        for event in events:
            err = kafka.Produce(event.topic, event.key, event.payload)
            if err == nil:
                UPDATE outbox_events SET status='sent'
            else:
                UPDATE outbox_events SET retries++, next_run_at=now()+backoff
}
```

**保证**：只要 DB 事务 COMMIT 成功，消息一定会被投递到 Kafka（Dispatcher 会持续重试直到成功）。

## 4. 消息有序性

### 保证链

```
写入顺序保证：
  Alice 发 M1 → outbox id=100
  Alice 发 M2 → outbox id=101
  （串行写入，id 单调递增）

Dispatcher 扫描顺序保证：
  SELECT ... ORDER BY id ASC
  → 先扫到 id=100 (M1)，再扫到 id=101 (M2)

Kafka partition 内顺序保证：
  key = "bob-coder@example"（接收方 agent_id）
  → 同一个 key 的消息路由到同一个 partition
  → partition 内 offset 严格递增
  → M1 offset=50, M2 offset=51

Consumer 消费顺序保证：
  eachMessage 串行处理（不并发）
  → 先处理 M1，完成后再处理 M2
```

### 跨 Task 不保证全局序

```
Alice → Bob: M1 (task-1)
Charlie → Bob: M2 (task-2)

两条消息可能由不同 Messaging Svc 实例写入 outbox
→ 不同 Dispatcher 实例发 Kafka 的顺序不确定
→ Bob 可能先收到 M2 再收到 M1

但这是可接受的：
  - 不同 task 之间本来就没有因果关系
  - 单 task 内严格有序（同一个 task 的消息只能由两方串行追加）
```

## 5. 消息不丢失

### 每个环节的持久化保证

```
┌──────────────────────────────────────────────────────────────┐
│                    消息不丢失保证链                             │
├──────────────────────────────────────────────────────────────┤
│                                                              │
│  ① HTTP 请求到达                                             │
│     │  失败 → 客户端收到错误 → 重试                           │
│     ▼                                                        │
│  ② DB 事务 COMMIT                                            │
│     │  成功 = 数据持久化到 MySQL WAL                          │
│     │  失败 → 客户端收到错误 → 重试                           │
│     ▼                                                        │
│  ③ Outbox 表（同事务）                                       │
│     │  COMMIT 成功 = outbox 一定有记录                        │
│     ▼                                                        │
│  ④ Dispatcher 扫描                                           │
│     │  FOR UPDATE SKIP LOCKED → crash 后重启仍能扫到          │
│     ▼                                                        │
│  ⑤ Kafka produce                                             │
│     │  成功 → MarkSent（不再重试）                            │
│     │  失败 → IncrRetry + 指数退避（最多 10 次）              │
│     │  10 次都失败 → MarkFailed + 告警                        │
│     ▼                                                        │
│  ⑥ Kafka 持久化                                              │
│     │  acks=all + replication.factor=3                        │
│     │  7 天保留                                               │
│     ▼                                                        │
│  ⑦ Consumer 消费                                             │
│     │  处理完 → autoCommit offset                             │
│     │  处理中 crash → offset 没 commit → 重启后重新消费       │
│     │  → 靠 dedup 去重（不会重复处理）                        │
│     ▼                                                        │
│  ⑧ 消息送达 ✅                                               │
│                                                              │
└──────────────────────────────────────────────────────────────┘
```

### Dispatcher 重试策略

```
第 1 次失败 → 等 5s 重试
第 2 次失败 → 等 10s 重试
第 3 次失败 → 等 20s 重试
...
第 10 次失败 → MarkFailed（人工介入）

指数退避公式：delay = 5s × 2^retries
最大等待：5s × 2^9 = 2560s ≈ 42 分钟
```

## 6. 消息不重复（严格幂等）

### 三层防护

```
┌─────────────────────────────────────────────────────┐
│              消息不重复：三层防护                      │
├─────────────────────────────────────────────────────┤
│                                                     │
│  第 1 层：DB UNIQUE 约束（写入侧）                   │
│  ┌─────────────────────────────────────────┐        │
│  │ UNIQUE KEY uk_message_id (message_id)    │        │
│  │                                         │        │
│  │ 同一条消息写两次 DB：                    │        │
│  │   第 1 次：INSERT 成功                   │        │
│  │   第 2 次：UNIQUE 冲突 → 返回已有记录    │        │
│  │   → 不产生重复行                         │        │
│  └─────────────────────────────────────────┘        │
│                                                     │
│  第 2 层：消费者 Dedup Store（消费侧）               │
│  ┌─────────────────────────────────────────┐        │
│  │ 内存 Set + 磁盘持久化                    │        │
│  │ 滑动窗口：保留最近 500 条 message_id     │        │
│  │                                         │        │
│  │ 消息到达 → dedup.has(id)?               │        │
│  │   true  → 跳过（不触发 LLM 推理）       │        │
│  │   false → 处理 → dedup.mark(id)         │        │
│  │                                         │        │
│  │ Crash 恢复：                             │        │
│  │   重启 → load() 从磁盘恢复 Set          │        │
│  │   → 已处理的消息不会被重复处理           │        │
│  └─────────────────────────────────────────┘        │
│                                                     │
│  第 3 层：全局唯一 message_id（生成侧）              │
│  ┌─────────────────────────────────────────┐        │
│  │ 格式：{prefix}-{agent_id}-{timestamp}-   │        │
│  │       {8位随机}                          │        │
│  │                                         │        │
│  │ 碰撞概率：                               │        │
│  │   36^8 ≈ 2.8 万亿种组合                 │        │
│  │   × 毫秒级时间戳                        │        │
│  │   → 实际碰撞概率为零                    │        │
│  └─────────────────────────────────────────┘        │
│                                                     │
└─────────────────────────────────────────────────────┘
```

### Dedup Store 实现

```typescript
// ~/.agent-mesh/cursor/dedup/{agentID}
// 文件内容（每行一个已处理的 message_id）：
m-alice-planner@example-1779156223-x3l2ea0b
m-bob-coder@example-1779156230-5tc7k9mn
m-alice-planner@example-1779156350-abc12345
...

// 滑动窗口：超过 500 行时淘汰最旧的
// 原子写：先写 .tmp 再 rename（防止半写）
```

## 7. 消息积压处理

### 积压发生的位置和应对

```
┌─────────────────────────────────────────────────────────────┐
│                    积压处理策略                               │
├─────────────────────────────────────────────────────────────┤
│                                                             │
│  位置 1：Outbox 表积压（Kafka 不可用时）                     │
│  ┌───────────────────────────────────────────────┐          │
│  │ 原因：Kafka broker 宕机                        │          │
│  │ 表现：outbox_events 表 pending 记录持续增长    │          │
│  │                                               │          │
│  │ 处理：                                        │          │
│  │   - Dispatcher 持续重试（指数退避）            │          │
│  │   - Kafka 恢复后自动追上（50 msg/s 消化）     │          │
│  │   - 入口限流（50 req/s per agent）限制增长速度│          │
│  │                                               │          │
│  │ 最坏情况：                                    │          │
│  │   50 msg/s × 60s = 3000 条/分钟积压           │          │
│  │   Kafka 恢复后 1 分钟清完                     │          │
│  └───────────────────────────────────────────────┘          │
│                                                             │
│  位置 2：Kafka Consumer Lag（LLM 推理慢）                    │
│  ┌───────────────────────────────────────────────┐          │
│  │ 原因：Agent 推理 10-30s/条，消费速度 < 生产速度│          │
│  │ 表现：consumer lag 增长                        │          │
│  │                                               │          │
│  │ 处理：                                        │          │
│  │   - 串行消费 = 自然背压（不会 OOM）           │          │
│  │   - Kafka 保留 7 天（不会丢）                 │          │
│  │   - 延迟线性增长（10 条积压 = 100-300s 延迟） │          │
│  │                                               │          │
│  │ 优化方向（未来）：                            │          │
│  │   - 按 task 优先级排序                        │          │
│  │   - 多 partition + 多 consumer 并行           │          │
│  │   - 轻量消息（status query）走快速通道        │          │
│  └───────────────────────────────────────────────┘          │
│                                                             │
│  位置 3：入口限流（防止源头洪水）                            │
│  ┌───────────────────────────────────────────────┐          │
│  │ 机制：API Gateway per-agent token bucket       │          │
│  │   - 50 req/s 稳态                             │          │
│  │   - burst 100（允许短暂突发）                  │          │
│  │   - 超限 → 429 Too Many Requests              │          │
│  │                                               │          │
│  │ 效果：                                        │          │
│  │   - 单 agent 最多 50 msg/s 写入               │          │
│  │   - 100 agent 同时打满 = 5000 msg/s           │          │
│  │   - Kafka 轻松承受（百万级吞吐）              │          │
│  └───────────────────────────────────────────────┘          │
│                                                             │
└─────────────────────────────────────────────────────────────┘
```

## 8. 故障场景处理

### 场景 1：Messaging Svc Crash

```
时间线：
  T+0    Alice 发消息 → Messaging Svc 收到
  T+5ms  BEGIN TX → INSERT task_messages → INSERT outbox
  T+10ms Messaging Svc crash（COMMIT 前）

结果：
  - TX 自动 ROLLBACK → 业务数据和 outbox 都没写入
  - Alice 收到 HTTP 500 错误
  - Alice 的 meshd 重试（SDK 自动重试 tool call）
  - 重试时 Messaging Svc 已恢复 → 成功

消息丢失？❌ 不丢（事务保证原子性）
```

### 场景 2：Kafka Broker 全部宕机

```
时间线：
  T+0    消息写入 DB + outbox 成功
  T+1s   Dispatcher 扫到 → Kafka produce 失败
  T+6s   第 1 次重试 → 失败
  T+16s  第 2 次重试 → 失败
  ...
  T+42min 第 10 次重试 → 失败 → MarkFailed

  T+45min Kafka 恢复
  
结果：
  - 10 次重试内恢复 → 自动追上，无感知
  - 超过 10 次 → MarkFailed → 需要人工介入
  - 消息在 DB 里不丢（task_messages 表有完整数据）
  - Agent 可以通过 HTTP poll fallback 拉到消息

消息丢失？❌ 不丢（DB 是 source of truth）
```

### 场景 3：meshd Consumer Crash

```
时间线：
  T+0    Bob 的 consumer 收到消息 M1
  T+5ms  dedup check → 未处理过 → 开始处理
  T+10s  LLM 推理完成 → mesh_reply 发出
  T+10.1s dedup.mark(M1) → 持久化到磁盘
  T+10.2s Kafka autoCommit offset

  --- 如果在 T+5ms ~ T+10.1s 之间 crash ---

  T+10.5s meshd 重启
  T+11s   Kafka consumer 从上次 committed offset 开始
  T+11.1s 重新收到 M1
  T+11.2s dedup.has(M1)?
           - 如果 mark 成功了 → true → 跳过 ✅
           - 如果 mark 没成功 → false → 重新处理（at-least-once）
             → 但 LLM 有 session 上下文，大概率不会重复回复

消息丢失？❌ 不丢
消息重复？极低概率（crash 窗口内 + mark 未持久化）
```

### 场景 4：Identity Svc 不可用

```
时间线：
  T+0    Alice 发消息 → Messaging Svc
  T+5ms  gRPC → Identity Svc: CanCommunicate(alice, bob)
  T+3s   gRPC 超时（3s timeout interceptor）

降级策略：
  - Messaging Svc 查 Redis 缓存（好友关系 TTL 30s）
  - 缓存命中 → 放行
  - 缓存未命中 → 返回 503 → meshd 重试

消息丢失？❌ 不丢（重试 or 缓存降级）
```

### 场景 5：消息积压导致延迟

```
场景：Bob 同时收到 10 条消息（来自不同 agent）

处理：
  M1 → 推理 15s → 回复
  M2 → 推理 10s → 回复
  ...
  M10 → 推理 20s → 回复

  M10 的端到端延迟 = 前 9 条推理时间之和 + 自己的推理时间
                   ≈ 9×15s + 20s = 155s

这是可接受的：
  - Agent 协作是异步的（不是实时聊天）
  - 发送方不会 block 等回复
  - 如果需要更低延迟 → 未来加多 partition + 并行 consumer
```

## 9. Task 活跃超时

Agent 是长任务模型——一个 task 可能持续几分钟到几小时。不能用简单的超时控制。

### 设计：活跃心跳 + TTL

```
┌─────────────────────────────────────────────────────┐
│              Task 活跃超时机制                        │
├─────────────────────────────────────────────────────┤
│                                                     │
│  每次 task 有新动作时刷新 updated_at：               │
│                                                     │
│  AppendMessage  → UPDATE task SET task_id=task_id   │
│                   (触发 ON UPDATE CURRENT_TIMESTAMP) │
│  AppendArtifact → TouchActivity(task_id)            │
│  Transition     → TransitionStatus 本身就 UPDATE    │
│                                                     │
│  定时扫描（每 5 分钟）：                             │
│  SELECT * FROM reliable_async_tasks                 │
│  WHERE status IN ('submitted','working')            │
│    AND updated_at < NOW() - INTERVAL 24 HOUR       │
│                                                     │
│  → 超过 24h 无任何活动 → 标记 failed                │
│  → 通知双方 agent                                   │
│                                                     │
│  正在活跃的 task（每隔几秒有新消息）：               │
│  → updated_at 持续刷新                              │
│  → 永远不会被超时                                   │
│                                                     │
└─────────────────────────────────────────────────────┘
```

## 10. 监控指标（应有）

```
# Outbox 积压深度
agent_mesh_outbox_pending_total{status="pending"}

# Kafka consumer lag
agent_mesh_consumer_lag{agent_id="bob-coder@example", topic="inbox.events"}

# 消息端到端延迟（从写入 outbox 到 consumer 处理完）
agent_mesh_message_e2e_latency_seconds{quantile="0.99"}

# Dispatcher 发送成功/失败率
agent_mesh_outbox_dispatch_total{result="sent|failed"}

# Dedup 命中率（重复消息被拦截的比例）
agent_mesh_dedup_hit_total{agent_id="..."}

# Task 超时数量
agent_mesh_task_timeout_total
```

## 11. 代码实现参考

以上设计在项目中的实际落地代码，供对照阅读：

### Outbox Dispatcher 核心结构

```go
// gateway/internal/domain/outbox/dispatcher.go

type Dispatcher struct {
    repo    Repo
    handler Handler  // 通常是 Kafka publish
    log     *zap.Logger
}

type Handler func(ctx context.Context, event *Event) error

type Event struct {
    ID          int64
    EventType   string           // e.g. "inbox.message:bob-coder@example"
    Payload     json.RawMessage
    Status      Status           // pending | sent | failed
    Retries     int
    NextRetryAt *time.Time
    CreatedAt   time.Time
    SentAt      *time.Time
}
```

Dispatcher 每秒轮询 `outbox_events` 表，批量取 50 条 pending 事件，逐条调用 Handler（发 Kafka）。失败时指数退避重试，最多 10 次。

### Kafka Producer 配置

```go
// gateway/internal/infra/kafka/producer.go

func NewProducer(brokers []string, log *zap.Logger) *Producer {
    w := &kafka.Writer{
        Addr:         kafka.TCP(brokers...),
        Balancer:     &kafka.Hash{},  // 按 key hash 分区，保证同一 agent 的消息有序
        Async:        true,
        BatchTimeout: 10 * time.Millisecond,
        BatchSize:    100,
    }
    return &Producer{writer: w, log: log}
}
```

`kafka.Hash{}` balancer 确保同一个 agent_id 的消息路由到同一个 partition，这是有序性保证的关键。

### 消息幂等写入（DB UNIQUE 约束）

```go
// gateway/internal/domain/task/repo.go

func (r *SQLRepo) AppendMessage(ctx context.Context, m *Message) (*Message, error) {
    _, err := r.db.ExecContext(ctx, `
        INSERT INTO task_messages
            (message_id, task_id, context_id, role, parts_json, ...)
        VALUES (?, ?, ?, ...)`,
        m.MessageID, m.TaskID, m.ContextID, string(m.Role), ...)
    if err != nil {
        if isDup(err) {
            // UNIQUE 冲突 → 检查是否是同一条消息的重试
            existing, _ := r.GetMessageByID(ctx, m.MessageID)
            if existing.TaskID == m.TaskID && existing.Role == m.Role {
                return existing, nil  // 幂等：返回已有记录
            }
            return nil, ErrMessageIDDuplicate  // 真正的 ID 碰撞
        }
        return nil, err
    }
    // ...
}
```

### Outbox 与业务事务的绑定

```go
// gateway/cmd/messaging-svc/main.go

// 业务写入和 outbox 在同一个事务中
taskSvc.WithOutbox(outboxRepo.AsTaskOutboxWriter())

// Dispatcher 独立 goroutine 轮询
kafkaProd := kafkaInfra.NewProducer(cfg.KafkaBrokers, log)
dispatcher := outbox.NewDispatcher(outboxRepo, func(ctx context.Context, event *outbox.Event) error {
    return kafkaProd.Publish(ctx, "inbox.events", extractKey(event), event.Payload)
}, log)
go dispatcher.Run(bgCtx)
```

> 完整代码见 `gateway/internal/domain/outbox/` 和 `gateway/internal/infra/kafka/`。

---

## 12. 总结

| 保证 | 机制 | 代价 |
|---|---|---|
| **不丢失** | Outbox 原子写入 + Dispatcher 重试 + Kafka 持久化 | 延迟增加 ~1s（Dispatcher 扫描间隔） |
| **不重复** | DB UNIQUE + Consumer Dedup Store + 全局唯一 ID | 内存 + 磁盘存 500 条 ID |
| **有序** | Outbox id 递增 + Kafka partition by key + 串行消费 | 单 consumer 吞吐受限 |
| **不积压** | 入口限流 + 串行背压 + Kafka 7 天保留 | 高负载时延迟线性增长 |
| **活跃超时** | updated_at 心跳 + 定时扫描 | 每次写消息多一次 UPDATE |

**设计哲学**：宁可延迟高一点，也不丢消息。Agent 协作是异步长任务，秒级延迟完全可接受；但丢一条消息可能导致整个协作链路断裂。
