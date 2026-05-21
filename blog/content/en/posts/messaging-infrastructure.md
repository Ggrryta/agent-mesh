---
title: "Messaging Infrastructure: Building a Reliable Distributed Communication System for AI Agents"
date: 2026-05-19
draft: false
categories: ["Architecture"]
tags: ["kafka", "outbox", "message-queue", "idempotency", "distributed-systems"]
series: ["Architecture Deep Dive"]
summary: "A deep dive into Agent Mesh's messaging infrastructure — how Kafka + Transactional Outbox achieves no message loss, no duplicates, and ordered delivery, plus handling of various failure scenarios."
---

> This post details the messaging infrastructure design of the Agent Mesh project — how multiple AI agents achieve reliable, ordered, loss-free asynchronous communication through Kafka + Transactional Outbox.

## 1. Problem Background

The core challenge of multi-agent collaboration systems isn't LLM reasoning — it's **message delivery**. When Alice Agent wants to ask Bob Agent a question:

- Messages can't be lost (Bob must receive it)
- Messages can't be duplicated (Bob shouldn't answer the same question twice)
- Messages must be ordered (Alice sends M1, M2; Bob must receive them in order)
- Must not block (Alice can't wait idle for Bob's reply; she needs to continue working)
- Must handle backpressure (new messages can't be lost while Bob is doing long reasoning)

The traditional HTTP request-response model can't meet these requirements — agent reasoning takes 10-30 seconds; synchronous calls won't work.

## 2. Overall Architecture

```
┌─────────────────────────────────────────────────────────────────────────┐
│                         Complete Message Chain                            │
│                                                                         │
│  Alice (meshd)          API Gateway         Messaging Svc               │
│  ┌──────────┐          ┌──────────┐        ┌──────────────┐            │
│  │ LLM      │─ HTTP ──▶│  :8080   │──route─▶│    :8082     │            │
│  │ Reasoning │          │ Rate/Auth│        │              │            │
│  │           │          └──────────┘        │  ┌────────┐  │            │
│  │ mesh_send │                              │  │ BEGIN  │  │            │
│  │ _message  │                              │  │  TX    │  │            │
│  └──────────┘                               │  ├────────┤  │            │
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
│                                             │  │(1s poll)│  │            │
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
│  │  │ Check   │  │Event │  │     │ │                                   │
│  │  └─────────┘  └──────┘  └──┬──┘ │                                   │
│  │                             │     │                                   │
│  │                        mesh_reply │                                   │
│  │                             │     │                                   │
│  └─────────────────────────────┼─────┘                                   │
│                                │                                         │
│                                ▼                                         │
│                    (Same chain back to Alice)                             │
└─────────────────────────────────────────────────────────────────────────┘
```

## 3. Core Design: Transactional Outbox

### Why Not Write Directly to Kafka?

```go
// ❌ Wrong: DB write and Kafka send are not atomic
func AppendMessage(ctx, msg) {
    db.Insert(msg)           // ← succeeds
    kafka.Produce(msg)       // ← if this fails, message is in DB but not in Kafka
                             //    receiver never knows about the new message
}
```

DB transactions and Kafka produce are two independent systems that can't be wrapped in a single transaction.

### Outbox Pattern Solves Atomicity

```go
// ✅ Correct: Business data and delivery instruction in the same transaction
func AppendMessage(ctx, msg) {
    tx := db.Begin()
    tx.Insert("task_messages", msg)              // Business data
    tx.Insert("outbox_events", {                 // Delivery instruction
        event_type: "inbox.message:bob",
        payload: serialize(msg),
    })
    tx.Commit()  // Atomic: either both succeed or both fail
}

// Background Dispatcher (independent goroutine)
func Dispatcher.Run() {
    every 1 second:
        events = SELECT ... FROM outbox_events
                 WHERE status='pending'
                 FOR UPDATE SKIP LOCKED  // Safe for multiple instances
                 LIMIT 50

        for event in events:
            err = kafka.Produce(event.topic, event.key, event.payload)
            if err == nil:
                UPDATE outbox_events SET status='sent'
            else:
                UPDATE outbox_events SET retries++, next_run_at=now()+backoff
}
```

**Guarantee**: As long as the DB transaction COMMITs successfully, the message will definitely be delivered to Kafka (Dispatcher retries until success).

## 4. Message Ordering

### Guarantee Chain

```
Write order guarantee:
  Alice sends M1 → outbox id=100
  Alice sends M2 → outbox id=101
  (Serial writes, id monotonically increasing)

Dispatcher scan order guarantee:
  SELECT ... ORDER BY id ASC
  → Scans id=100 (M1) first, then id=101 (M2)

Kafka partition order guarantee:
  key = "bob-coder@example" (recipient agent_id)
  → Same key routes to same partition
  → Offsets strictly increasing within partition
  → M1 offset=50, M2 offset=51

Consumer consumption order guarantee:
  eachMessage processed serially (no concurrency)
  → Process M1 first, then M2 after completion
```

### No Global Order Across Tasks

```
Alice → Bob: M1 (task-1)
Charlie → Bob: M2 (task-2)

Two messages may be written to outbox by different Messaging Svc instances
→ Different Dispatcher instances send to Kafka in unpredictable order
→ Bob may receive M2 before M1

But this is acceptable:
  - Different tasks have no causal relationship
  - Strict ordering within a single task (messages can only be appended serially by both parties)
```

## 5. No Message Loss

### Persistence Guarantee at Every Stage

```
┌──────────────────────────────────────────────────────────────┐
│                    No Message Loss Guarantee Chain             │
├──────────────────────────────────────────────────────────────┤
│                                                              │
│  ① HTTP request arrives                                      │
│     │  Failure → client gets error → retry                   │
│     ▼                                                        │
│  ② DB transaction COMMIT                                     │
│     │  Success = data persisted to MySQL WAL                 │
│     │  Failure → client gets error → retry                   │
│     ▼                                                        │
│  ③ Outbox table (same transaction)                           │
│     │  COMMIT success = outbox definitely has a record       │
│     ▼                                                        │
│  ④ Dispatcher scan                                           │
│     │  FOR UPDATE SKIP LOCKED → still scannable after crash  │
│     ▼                                                        │
│  ⑤ Kafka produce                                             │
│     │  Success → MarkSent (no more retries)                  │
│     │  Failure → IncrRetry + exponential backoff (max 10)    │
│     │  10 failures → MarkFailed + alert                      │
│     ▼                                                        │
│  ⑥ Kafka persistence                                         │
│     │  acks=all + replication.factor=3                       │
│     │  7-day retention                                       │
│     ▼                                                        │
│  ⑦ Consumer consumption                                      │
│     │  Processed → autoCommit offset                         │
│     │  Crash during processing → offset not committed        │
│     │  → re-consumed after restart                           │
│     │  → dedup prevents duplicate processing                 │
│     ▼                                                        │
│  ⑧ Message delivered ✅                                      │
│                                                              │
└──────────────────────────────────────────────────────────────┘
```

### Dispatcher Retry Strategy

```
1st failure → wait 5s retry
2nd failure → wait 10s retry
3rd failure → wait 20s retry
...
10th failure → MarkFailed (manual intervention)

Exponential backoff formula: delay = 5s × 2^retries
Maximum wait: 5s × 2^9 = 2560s ≈ 42 minutes
```

## 6. No Duplicates (Strict Idempotency)

### Three-Layer Defense

```
┌─────────────────────────────────────────────────────┐
│              No Duplicates: Three-Layer Defense       │
├─────────────────────────────────────────────────────┤
│                                                     │
│  Layer 1: DB UNIQUE Constraint (write side)          │
│  ┌─────────────────────────────────────────┐        │
│  │ UNIQUE KEY uk_message_id (message_id)    │        │
│  │                                         │        │
│  │ Same message written twice to DB:        │        │
│  │   1st time: INSERT succeeds              │        │
│  │   2nd time: UNIQUE conflict → return     │        │
│  │             existing record              │        │
│  │   → No duplicate rows                   │        │
│  └─────────────────────────────────────────┘        │
│                                                     │
│  Layer 2: Consumer Dedup Store (consume side)        │
│  ┌─────────────────────────────────────────┐        │
│  │ In-memory Set + disk persistence          │        │
│  │ Sliding window: keep last 500 message_ids │        │
│  │                                         │        │
│  │ Message arrives → dedup.has(id)?         │        │
│  │   true  → skip (don't trigger LLM)      │        │
│  │   false → process → dedup.mark(id)      │        │
│  │                                         │        │
│  │ Crash recovery:                          │        │
│  │   Restart → load() from disk             │        │
│  │   → Already-processed messages won't be  │        │
│  │     reprocessed                          │        │
│  └─────────────────────────────────────────┘        │
│                                                     │
│  Layer 3: Globally Unique message_id (generation)    │
│  ┌─────────────────────────────────────────┐        │
│  │ Format: {prefix}-{agent_id}-{timestamp}- │        │
│  │         {8-char random}                  │        │
│  │                                         │        │
│  │ Collision probability:                   │        │
│  │   36^8 ≈ 2.8 trillion combinations      │        │
│  │   × millisecond timestamp               │        │
│  │   → Practical collision probability: zero│        │
│  └─────────────────────────────────────────┘        │
│                                                     │
└─────────────────────────────────────────────────────┘
```

## 7. Backpressure Handling

### Where Backpressure Occurs and How to Handle It

```
┌─────────────────────────────────────────────────────────────┐
│                    Backpressure Strategy                      │
├─────────────────────────────────────────────────────────────┤
│                                                             │
│  Location 1: Outbox Table Backlog (Kafka unavailable)        │
│  ┌───────────────────────────────────────────────┐          │
│  │ Cause: Kafka broker down                       │          │
│  │ Symptom: outbox_events pending records growing │          │
│  │                                               │          │
│  │ Handling:                                     │          │
│  │   - Dispatcher keeps retrying (exp backoff)   │          │
│  │   - Auto catches up when Kafka recovers       │          │
│  │   - Ingress rate limiting (50 req/s/agent)    │          │
│  │     limits growth rate                        │          │
│  └───────────────────────────────────────────────┘          │
│                                                             │
│  Location 2: Kafka Consumer Lag (slow LLM reasoning)         │
│  ┌───────────────────────────────────────────────┐          │
│  │ Cause: Agent reasons 10-30s/msg, consume <    │          │
│  │        produce rate                           │          │
│  │ Symptom: consumer lag grows                   │          │
│  │                                               │          │
│  │ Handling:                                     │          │
│  │   - Serial consumption = natural backpressure │          │
│  │   - Kafka retains 7 days (won't lose)         │          │
│  │   - Latency grows linearly (10 backlog =      │          │
│  │     100-300s delay)                           │          │
│  └───────────────────────────────────────────────┘          │
│                                                             │
│  Location 3: Ingress Rate Limiting (prevent flood)           │
│  ┌───────────────────────────────────────────────┐          │
│  │ Mechanism: API Gateway per-agent token bucket  │          │
│  │   - 50 req/s steady state                     │          │
│  │   - burst 100 (allow short spikes)            │          │
│  │   - Over limit → 429 Too Many Requests        │          │
│  └───────────────────────────────────────────────┘          │
│                                                             │
└─────────────────────────────────────────────────────────────┘
```

## 8. Failure Scenario Handling

### Scenario 1: Messaging Svc Crash

```
Timeline:
  T+0    Alice sends message → Messaging Svc receives
  T+5ms  BEGIN TX → INSERT task_messages → INSERT outbox
  T+10ms Messaging Svc crashes (before COMMIT)

Result:
  - TX auto ROLLBACK → neither business data nor outbox written
  - Alice receives HTTP 500 error
  - Alice's meshd retries (SDK auto-retries tool call)
  - On retry, Messaging Svc has recovered → success

Message lost? ❌ No (transaction guarantees atomicity)
```

### Scenario 2: All Kafka Brokers Down

```
Timeline:
  T+0    Message written to DB + outbox successfully
  T+1s   Dispatcher scans → Kafka produce fails
  T+6s   1st retry → fails
  T+16s  2nd retry → fails
  ...
  T+42min 10th retry → fails → MarkFailed

  T+45min Kafka recovers

Result:
  - Recovery within 10 retries → auto catches up, transparent
  - Beyond 10 retries → MarkFailed → manual intervention needed
  - Message in DB is not lost (task_messages is source of truth)
  - Agent can use HTTP poll fallback to get messages

Message lost? ❌ No (DB is source of truth)
```

### Scenario 3: meshd Consumer Crash

```
Timeline:
  T+0    Bob's consumer receives message M1
  T+5ms  dedup check → not processed → begin processing
  T+10s  LLM reasoning complete → mesh_reply sent
  T+10.1s dedup.mark(M1) → persisted to disk
  T+10.2s Kafka autoCommit offset

  --- If crash between T+5ms ~ T+10.1s ---

  T+10.5s meshd restarts
  T+11s   Kafka consumer resumes from last committed offset
  T+11.1s Receives M1 again
  T+11.2s dedup.has(M1)?
           - If mark succeeded → true → skip ✅
           - If mark didn't succeed → false → reprocess (at-least-once)
             → But LLM has session context, unlikely to duplicate reply

Message lost? ❌ No
Message duplicated? Extremely low probability (crash window + mark not persisted)
```

## 9. Task Activity Timeout

Agents are long-task models — a task may last minutes to hours. Simple timeouts won't work.

### Design: Activity Heartbeat + TTL

```
┌─────────────────────────────────────────────────────┐
│              Task Activity Timeout Mechanism          │
├─────────────────────────────────────────────────────┤
│                                                     │
│  Refresh updated_at on every task action:            │
│                                                     │
│  AppendMessage  → UPDATE task SET task_id=task_id   │
│                   (triggers ON UPDATE TIMESTAMP)     │
│  AppendArtifact → TouchActivity(task_id)            │
│  Transition     → TransitionStatus itself UPDATEs   │
│                                                     │
│  Periodic scan (every 5 minutes):                    │
│  SELECT * FROM reliable_async_tasks                 │
│  WHERE status IN ('submitted','working')            │
│    AND updated_at < NOW() - INTERVAL 24 HOUR       │
│                                                     │
│  → Over 24h with no activity → mark failed          │
│  → Notify both agents                              │
│                                                     │
│  Actively running task (new message every few secs): │
│  → updated_at continuously refreshed                │
│  → Will never time out                             │
│                                                     │
└─────────────────────────────────────────────────────┘
```

## 10. Monitoring Metrics

```
# Outbox backlog depth
agent_mesh_outbox_pending_total{status="pending"}

# Kafka consumer lag
agent_mesh_consumer_lag{agent_id="bob-coder@example", topic="inbox.events"}

# End-to-end message latency (from outbox write to consumer processing)
agent_mesh_message_e2e_latency_seconds{quantile="0.99"}

# Dispatcher send success/failure rate
agent_mesh_outbox_dispatch_total{result="sent|failed"}

# Dedup hit rate (proportion of duplicate messages intercepted)
agent_mesh_dedup_hit_total{agent_id="..."}

# Task timeout count
agent_mesh_task_timeout_total
```

## 11. Code Implementation Reference

The designs above are implemented in the project codebase. Key references:

### Outbox Dispatcher Core Structure

```go
// gateway/internal/domain/outbox/dispatcher.go

type Dispatcher struct {
    repo    Repo
    handler Handler  // Typically Kafka publish
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

### Kafka Producer Configuration

```go
// gateway/internal/infra/kafka/producer.go

func NewProducer(brokers []string, log *zap.Logger) *Producer {
    w := &kafka.Writer{
        Addr:         kafka.TCP(brokers...),
        Balancer:     &kafka.Hash{},  // Hash by key for partition routing
        Async:        true,
        BatchTimeout: 10 * time.Millisecond,
        BatchSize:    100,
    }
    return &Producer{writer: w, log: log}
}
```

### Idempotent Message Write (DB UNIQUE Constraint)

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
            existing, _ := r.GetMessageByID(ctx, m.MessageID)
            if existing.TaskID == m.TaskID && existing.Role == m.Role {
                return existing, nil  // Idempotent: return existing record
            }
            return nil, ErrMessageIDDuplicate
        }
        return nil, err
    }
    // ...
}
```

> Full code at `gateway/internal/domain/outbox/` and `gateway/internal/infra/kafka/`.

---

## 12. Summary

| Guarantee | Mechanism | Cost |
|---|---|---|
| **No loss** | Outbox atomic write + Dispatcher retry + Kafka persistence | ~1s added latency (Dispatcher scan interval) |
| **No duplicates** | DB UNIQUE + Consumer Dedup Store + globally unique ID | Memory + disk for 500 IDs |
| **Ordered** | Outbox id ascending + Kafka partition by key + serial consumption | Single consumer throughput limited |
| **No backlog overflow** | Ingress rate limiting + serial backpressure + Kafka 7-day retention | Latency grows linearly under high load |
| **Activity timeout** | updated_at heartbeat + periodic scan | One extra UPDATE per message write |

**Design philosophy**: Better to have slightly higher latency than to lose messages. Agent collaboration is asynchronous long-task work — second-level latency is perfectly acceptable; but losing one message could break an entire collaboration chain.
