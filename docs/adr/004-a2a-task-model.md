# ADR 004: Task 数据模型对齐 A2A 协议

**Status**: Accepted
**Date**: 2026-05-12
**Relates to**: 002-task-poll-vs-outbox (扫表版 Task 实现) / 003-outbox-for-fanout (群消息 fan-out)

## 背景

Week 1 基线 migration（`0001_init.sql`）里把 `reliable_async_tasks` 设计成"一条记录
表示一次单轮调用"——扁平的 `input JSON` / `output JSON` 两列。

在审计 A2A 协议规范（
[Life of a Task](https://github.com/a2aproject/A2A/blob/main/docs/topics/life-of-a-task.md)
和
[proto spec](https://github.com/a2aproject/A2A/blob/main/specification/a2a.proto)
）后发现，这个扁平设计**严重偏离 A2A 的 Task 语义**：

| 协议里的 Task | 当前扁平设计 |
|---|
| 有 `contextId` 串联多 Task 对话 | 无 |
| 有 `history[]` Messages 记录多轮对话 | 只有一个 `input JSON` |
| 有 `artifacts[]` 多个可交付产物 | 只有一个 `output JSON` |
| 支持 `input-required` 中间态恢复 | 状态机无此分支 |
| 支持增量 artifact 流 | 无 |
| Artifact 有 `artifactId` + `name` 可引用 | 无法从外部引用 |

后果：

- 无法支持 A2A 客户端标准行为（例如 "把帆船改红色" 这种 follow-up
  refinement，需要用同一 `contextId` 发新 Task 并带 `referenceTaskIds`）
- 群组场景下，"一个群聊天 = 一个 context 下多 Task/多 Message" 的自然模型无法表达
- 未来接入真 A2A SDK（例如 GAS 侧的 Agent Core）需要把扁平 output 反推成
  `Task{artifacts[]}` 结构，损耗信息

## 决定

**将 Task 拆成三张表，对齐 A2A 数据模型**：

```
reliable_async_tasks    ← Task 主表（状态 + 调度元数据）
├── task_messages       ← Task.history[]（对话回合）
└── task_artifacts      ← Task.artifacts[]（可交付产物）
```

### `reliable_async_tasks`（瘦身后）

保留调度必需字段，移除 `input` / `output`：

```sql
task_id        VARCHAR(64)   -- A2A Task.id
context_id     VARCHAR(64)   -- A2A contextId（多 Task 会话关联）
from_agent_id  VARCHAR(64)   -- client 方（initiator）
to_agent_id    VARCHAR(64)   -- server 方（executing agent）
status         VARCHAR(32)   -- A2A TaskState:
                             -- submitted|working|input-required|auth-required|
                             -- completed|canceled|failed|rejected
                             -- 内部加 retrying（对应退避等待）
status_message VARCHAR(1024) -- TaskStatus.message 的文本快照
error_msg      VARCHAR(1024) -- 最后一次错误
retries        INT           -- 内部重试次数
next_run_at    DATETIME(3)   -- Worker 扫表兜底
claimed_at     DATETIME(3)   -- 僵尸识别
version        INT           -- 乐观锁
metadata_json  JSON          -- A2A Task.metadata
```

### `task_messages`（A2A history[]）

每一行 = Task 生命周期中的一条 Message（user 或 agent）：

```sql
message_id     VARCHAR(64)   -- A2A Message.messageId，全局唯一
task_id        VARCHAR(64)
context_id     VARCHAR(64)   -- 冗余，便于 context 视角查询
role           VARCHAR(16)   -- user | agent
parts_json     JSON          -- A2A Part[] 数组（text/raw/url/data 四种 oneof）
metadata_json  JSON          -- A2A Message.metadata
reference_task_ids JSON      -- A2A referenceTaskIds（follow-up 引用）
```

### `task_artifacts`（A2A artifacts[]）

每一行 = Task 产出的一个可交付物：

```sql
artifact_id    VARCHAR(64)   -- A2A Artifact.artifactId，在 Task 内唯一
task_id        VARCHAR(64)
context_id     VARCHAR(64)   -- 冗余，用于跨 Task 按 name 找最新
name           VARCHAR(128)  -- 稳定名字，Task refinement 时保持一致
description    VARCHAR(512)
parts_json     JSON          -- A2A Part[]
metadata_json  JSON
```

`(context_id, name)` 索引支持"在一个 context 里找最新命名 artifact"，对应协议里
`Tracking Artifact Mutation` 的客户端检索需求。

## 替代方案（已否决）

### 方案 A：保持扁平，不对齐 A2A

**不选**：Agent-Mesh 的核心命题就是"A2A skill 协议的 agent 零改造接入"，数据
模型和协议脱节违背项目定位；未来接入真 A2A Client/Server SDK 时要反复做
shape conversion。

### 方案 C：完整 A2A 原生 + Artifact Part 级增量表

在方案 B 基础上再拆 `artifact_parts`，支持流式 Part 增量拼接、
`task_references`、`context_participants` 等。

**不选**：MVP 阶段不需要。流式增量在客户端一次性拿全量也工作，
`task_artifacts.parts_json` 直接存整个 Part 数组即可。跨 Task 引用的
`referenceTaskIds` 已经落在 `task_messages` 里了，够用。

## 影响

### 架构

- Task Worker 的扫表 / Claim / retry 逻辑**不变**（仍只作用于 `reliable_async_tasks` 主表）
- 新增操作：`AppendMessage(taskID, msg)` / `AppendArtifact(taskID, artifact)` / `TransitionStatus(taskID, ...)`
- 状态机新增 `input-required` 分支：Worker 返回中间态时挂起任务，等待客户端追加 Message 后自动恢复

### API

Mesh API 新形态（Week 3 Day 4 落地）：

```
POST /v1/mesh/tasks                              创建 Task（首条 Message 随 body）
POST /v1/mesh/tasks/{id}/messages                追加 Message（input-required 恢复 / 多轮对话）
GET  /v1/mesh/tasks/{id}?include=history,artifacts   按需返回完整 Task
POST /v1/mesh/tasks/{id}/cancel                  转 canceled
GET  /v1/mesh/tasks?context_id=...               按 context 查一组相关 Task
```

### 群组消息的复用

Week 4 的群组消息**不建独立 `messages` 表**，复用 `task_messages`：

- 一个群 ↔ 一个 A2A `contextId`
- 群消息 = `task_messages` 行 + `metadata_json.group_id`
- 群聊时间线 = `WHERE context_id=group.context_id ORDER BY created_at`
- 用 `outbox_events` fan-out 到各成员的 SSE

**收益**：数据模型统一，Task 和群消息共享同一套索引/历史/幂等保证。

### 工作量

Week 3 原为 5 天扫表版任务，新增：

- Day 1 多加 `task_messages` / `task_artifacts` repo 方法
- Day 3 加入 `input-required` 分支实现
- Day 4 API 从"提交+查询"扩展到"追加消息+取历史+取产物"
- 整体多 1 天，仍可在 Week 3 的 5+2 天内完成

## 遗留不做

- **Artifact Part 级增量 streaming**（方案 C）
- **跨 Task Artifact 版本链**：协议明确由 client 负责跟踪，网关不做
- **contextId 生命周期管理**：何时创建/归档，靠业务层（Week 4 群组 + Week 6
  前端对话视图）决定，协议/schema 不限制

## Week 3 实现补充（2026-05-13）

Week 3 按本 ADR 实现了 task / message / artifact 三张表的 domain + API。
对齐 ADR 002（Gateway 不执行任务）后，原设计里的几个字段被**保留但不使用**：

| 字段（migration 0001 已有） | 原用途 | 现状 |
|---|---|---|
| `retries` | Worker 重试次数 | 不使用，保留 schema 兼容 |
| `next_run_at` | Worker 下次执行时间 | 不使用 |
| `claimed_at` | Worker 抢占时间戳 | 不使用 |
| `version` | 乐观锁版本号 | 不使用（用 status CAS 替代） |

这些字段在未来若需要"Gateway 侧兜底执行"（见 ADR 002 演进章节）时可以复用，
届时**不用改 schema**。

### Message.role 决定策略
API 层**不接受** caller 传入 role；由 service 根据 caller 在 Task 中的
身份自动推断：
- caller = from_agent_id → role=user
- caller = to_agent_id → role=agent

这样防止服务 agent 伪造 user 回复或反之。

### Message / Artifact 的 JSON tag
model.go 里三个结构体的 JSON tag 全部和 A2A 协议字段名对齐：
- `MessageID` → `message_id`
- `TaskID` → `task_id`
- `RefTaskIDs` → `reference_task_ids`
- ID 内部自增不外暴（`json:"-"`）

inbox 事件的 payload 直接 marshal 这些结构体，agent SDK 反序列化即可用。

### 状态机合法转换表
在 model.go 的 `allowedTransitions` 常量里：

```
submitted      → working / canceled / rejected
working        → completed / failed / canceled / input-required / auth-required
input-required → submitted / working / canceled
auth-required  → submitted / canceled
终态           → ∅
```

`TransitionStatus` 的 SQL：
```sql
UPDATE reliable_async_tasks
SET status = ?, status_message = ?, error_msg = ?
WHERE status IN (...allowed froms...) AND task_id = ?
```
`RowsAffected=1` 代表转换成功；否则 `ErrInvalidTransition`，并发安全。
