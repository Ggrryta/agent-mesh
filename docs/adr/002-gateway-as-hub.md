# ADR 002：Gateway 是消息中枢，不是任务执行者

- **状态**：Accepted
- **日期**：2026-05-13
- **取代了**：原始 PLAN v1/v2 里 "Gateway 跑 Worker 执行 task" 的设计

## 背景

早期设计里把 `reliable_async_tasks` 当成"后台作业队列"：Gateway 里跑一个
Worker，扫表 → CAS Claim → 调用 Bob 的 URL → 写结果 → 转状态。原项目
`reliable_task_worker` 也是这套。

但重新对齐 A2A 协议后发现这套模型有本质错位：

1. **Task 不是作业，是 A2A 协议里的一等业务实体**：有 history、artifacts、
   contextId；两端 agent 可查、可追加、可取消
2. **Task 的"执行者"是被叫 agent（Bob）自己**：Bob 有自己的业务逻辑、
   checkpoint、crash 恢复、子任务协调。Gateway 不应该越俎代庖
3. **input-required 是 workflow 级的挂起**：任务中途暂停等 user 说话；
   Worker 模型的"领任务 → 跑完 → 回结果"做不到
4. **Gateway 里做 Worker 的副作用**：
   - Worker 要调 Bob 的 URL → Bob 的网络拓扑（NAT、云函数）受限
   - Worker crash 了要孤儿回滚 → 多一个失败模式
   - Worker 执行失败要重试 → 重试策略耦合业务
   - Gateway 要"理解" Bob 的响应才能转状态 → Gateway 被迫学业务语义

## 决策

**Gateway 是消息中枢，不是任务执行者。**

### Gateway 的职责（只做四件事）

1. **持久化 Task 事实**
   - `reliable_async_tasks` 表：task_id / context_id / from / to / status
   - `task_messages` 表：每一轮对话（Task.history[]）
   - `task_artifacts` 表：被叫 agent 产出的交付物（Task.artifacts[]）
2. **路由消息与产物**：把任何一端的动作（新 message、新 artifact、状态变更）
   写入对方的 inbox，让对方能拿到
3. **送达保证**：inbox 是真相之源；对有 URL 的 agent 另外尝试 push
4. **最小语义校验**：状态转换合法性（`completed` 不能回 `working`）、
   授权（`friendship.AreFriends`）、幂等（`message_id` UNIQUE）

### Gateway 不做的事

| 项 | 由谁做 |
|---|---|
| 调 agent 的 URL 去执行任务 | **Bob（被叫 agent）自己调自己业务** |
| 推动状态机（working → completed） | **Alice / Bob 各自 POST /transition** |
| 重试失败的任务 | **agent 自己决定是否重试**（新一轮 task 或新一条 message） |
| 任务超时判定（input-required 放多久） | **agent 自己决定**（超时就 cancel） |
| 任务执行结果合并 / 后处理 | **Alice 收到 artifact 后自己处理** |
| Worker 心跳 / 孤儿回滚 | **Gateway 不占任务**，所以不存在孤儿 |

### 信息流

```
Alice agent                Gateway                     Bob agent
───────────                ───────                     ─────────
POST /tasks  ─────────────▶ INSERT task
                            INSERT message (role=user)
                            Enqueue bob.inbox
                            push 尝试 (optional) ─────▶ POST /a2a/events
                                                        (bob 开始执行业务)

                                                        POST /tasks/{id}/transition
                                                        (bob → "working")
                            校验状态机合法性 ◀──────────
                            UPDATE task.status
                            Enqueue alice.inbox
                            push ──────────────────────▶

                                                        POST /tasks/{id}/artifacts
                            INSERT artifact  ◀──────────
                            Enqueue alice.inbox
                            push ──────────────────────▶

                                                        POST /tasks/{id}/transition
                                                        (bob → "completed")
                            校验状态机 ◀────────────────
                            UPDATE task.status
                            Enqueue alice.inbox
                            push ──────────────────────▶
                                                         
 (alice 拉 inbox)
 GET /inbox?since=X ───────▶ 返回积压事件
```

## 考虑过的替代

### A. Gateway 跑 Worker 执行任务（原设计）
- ❌ 违反 A2A 协议里 Gateway 的中立定位
- ❌ Gateway 被迫学业务语义
- ❌ input-required 做不了（Worker 必须释放后又能被 user message 唤醒，本质是 workflow）
- ❌ Gateway 和 Bob 的耦合度过高（调 URL、重试、超时全在 Gateway）

### B. 用 Asynq / River / Celery 之类的任务队列
- ❌ 这些框架把 task 当黑盒 job，不认 A2A 的 history / artifacts / contextId
- ❌ 业务表和框架 task 要双写同步
- ❌ 多引入 Redis/broker 基础设施

### C. 用 Temporal
- ❌ 运维成本巨大（集群 + storage + UI），MVP 不可承受
- ❌ 业务代码得用 Temporal SDK 写，和 Go 原生业务风格撕裂
- ✅ 语义上最贴近（workflow + 挂起 + 恢复），但代价不匹配收益
- 🔜 将来如果 task 复杂度真的上去了，可以把 `task.Service` 的底换成 Temporal

### D. Gateway 只做消息中枢（本决策）
- ✅ 职责清晰：Gateway 完全无业务语义
- ✅ A2A 协议原生对齐
- ✅ agent 侧可以任意实现（Python / Go / TS / 裸 HTTP），只要会用 /tasks 和 /inbox API
- ✅ 维护简单：没有 Worker / 重试 / 孤儿回滚要管
- ❌ 更多跨端协同的正确性压到 agent 侧（需要 SDK 封好）

## 决策后果

### 得
- Gateway 代码量砍半（原 Week 3 估 1700 行，现在估 1000~1200 行）
- 无 Worker / 无 Claim / 无心跳 / 无孤儿回滚
- 更符合 A2A 协议语义
- agent 实现自由（任何语言都能跑，不用嵌我们的执行 runtime）
- Week 4 SDK 的职责范围更清晰

### 失
- agent 侧责任变大：要自己做 checkpoint、处理 push 失败、幂等去重
  （解法：Week 4 GAS SDK 封装这些，agent 业务无感）
- 没有 Gateway 层的统一重试 —— 业务层面的"重发 task"由 agent 自己决定

### 约束给下游
- **所有状态变更必须由 agent 主动 POST** `/tasks/{id}/transition`，Gateway
  不会"自动" 把 submitted 转 working
- **送达是尽力而为**：push 失败不会自动重试；agent 必须有"拉 inbox 补漏"的能力
- **Gateway 不懂 artifact 的内容**：只存 parts 的 JSON，不解析 PDF / 图 / 代码

## 未来演进

1. **Gateway 内任务数超过单库承受**（> 10k submit/s）：
   把 `task.Repo` 改成分片或 TiDB；`task.Service` 签名不变
2. **agent 侧需要复杂 workflow 编排**（嵌套 task、子 task、事务性子流程）：
   单个 agent 内部嵌 Temporal，Gateway 层面无感
3. **需要送达保证强化**（金融级）：
   push 失败转 Outbox → MQ → 重试（此时用 Kafka / NATS），**不影响 Gateway 的消息中枢定位**

## 参考

- A2A 协议：https://github.com/a2aproject/A2A/blob/main/docs/topics/life-of-a-task.md
- ADR 004：Task 数据模型对齐 A2A（三表拆分）
- ADR 008：Friendship，Task 发送前校验的依据
- ADR 010：Inbox + 可选 push 的送达模型
