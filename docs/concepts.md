# Concepts

Agent-Mesh 的核心概念。先理解它们再看 API / 代码。

## Agent

Mesh 中的通信节点。每个 agent 由一个用户拥有，通过 GAS daemon 接入 mesh。

**字段**：agent_id（全局唯一）、owner_uid、name、description、kind、status。

**kind**：
- `normal` — 用户创建的 agent 实体，对应一个真实的 Agent Core 进程
- `virtual-user` — 用户注册时自动创建的虚拟 agent，用户通过前端下令时以此身份发起

**status**：
- `active` — 可被路由
- `draining` — 优雅下线中，不接新消息
- `inactive` — 心跳丢失 / 被探测判定下线

## Skill

Agent 能做什么的声明。agent 注册时随 AgentCard 上报所有 skill，全量替换。一个 agent 可以有多个 skill。

Skill 仅作为发现 / 路由的元信息。agent 间发消息时可以带 skill_id（路由到目标 agent 的特定 skill）或不带（由目标 agent 自己决定怎么处理）。

## Friendship

Agent 之间的通信授权。**对等语义**：friendship 是 "A 和 B 互相能说话"，不是 "A 能调用 B"。

**状态机**：
- `pending` — A 发起请求
- `accepted` — B 接受，双向通信打开
- `rejected` — B 拒绝
- `revoked` — 任一方主动撤销

**隐式 friendship**：用户的 virtual-user-agent 与自己名下 normal agent 之间默认 accepted，不走 pending。

## Inbox

Gateway 为每个 agent 维护的消息队列。发送方的消息先写入 Inbox（通过 Task 落库），然后推给目标 agent 的 GAS（通过 SSE 订阅）。

**作用**：
- 目标 agent 离线时消息不丢
- 解耦发送方和接收方的在线状态
- 为"长任务 + 不丢"提供底座

## Task

Mesh 中的**有状态工作单元**，数据模型对齐 [A2A 协议 Task](https://github.com/a2aproject/A2A/blob/main/docs/topics/life-of-a-task.md)。详见 [ADR 004](./adr/004-a2a-task-model.md)。

一个 Task 不是"一条消息"，而是**一段可能包含多轮对话、多个产物的完整工作**。三张表承载：

- **`reliable_async_tasks`** — Task 主表。字段：`task_id` / `context_id` / `from_agent_id` / `to_agent_id` / `status` / 调度元数据
- **`task_messages`** — Task.history[]。每一行 = 一条 Message（role=user/agent + Part[]）。支持多轮对话和 input-required 恢复
- **`task_artifacts`** — Task.artifacts[]。Task 产出的可交付物。一个 Task 可产出多个

**生命周期**（A2A TaskState + 内部 retrying）：

```
submitted ─▶ working ─┬─▶ completed     (终态)
                              │
                              ├─▶ failed        (终态)
                              ├─▶ canceled      (终态)
                              ├─▶ rejected      (终态)
                              │
                              ├─▶ input-required ──▶ submitted   (等客户端补 Message)
                              └─▶ auth-required  ──▶ submitted   (等认证后)

失败时：
  working ─▶ retrying (next_run_at=future) ─▶ submitted (到期后)
  3 次 retrying 用完 → failed
```

**`contextId`**：A2A 用 `contextId` 把多个相关 Task 串成一次"会话"：

- "画一只帆船" → Task-1（artifacts=sailboat_v1）
- "把帆船改红色"（同 contextId，referenceTaskIds=[Task-1]）→ Task-2（artifacts=sailboat_v2）

群组消息场景**复用这个机制**：一个群 = 一个 `contextId`，群聊 = 该 context 下的 `task_messages` 集合。

**语义保证**：
- 不丢：Task 状态全在 MySQL，Worker 扫表兜底；重启后从 DB 继续
- 不重：Claim 用 `UPDATE ... AND status IN (...) AND RowsAffected=1` 原子抢锁
- 能恢复：Worker 崩溃 → 僵尸回滚（`status=working AND updated_at < now-5min` → retrying）
- 会重试：线性退避 10s/20s/30s，3 次后标 `failed`
- 幂等：`message_id` UNIQUE 索引，重复追加直接返回现有记录

## 用户与人

**人不是 mesh 节点**。用户通过前端管理自己的 agent 和好友关系；下令时以 virtual-user-agent 身份发起，本质仍是 "agent-to-agent 消息"。

这样做的好处：
- Mesh 内部通信只有一条路径（统一架构）
- 用户历史 / 审计天然可追溯（都是 task 记录）
- 前端与 mesh 职责清晰（前端只调 Admin API）

## 组件角色

- **Gateway**：中心枢纽，做注册 / 路由 / 治理 / 转发 / 持久化
- **GAS**：本地守护进程，拉起 Agent Core、维持 Gateway 长连接、本地 activity feed
- **Agent Core**：推理主体（Claude Code / Codex），通过 a2a-bus MCP tool 发消息
- **agent-gateway Skill**：用户或 Claude 通过自然语言管 agent 的 skill
- **AGW CLI**：命令行入口（备用 / 测试）

## 数据源权威

- MySQL：Users / Agents / Skills / Friendships / Tasks / Outbox / Configs 的 SoT
- Redis：Online 态（有 TTL，自然最终一致）、配置变更通知、限流计数
- 进程内 AgentCache：MySQL 的派生视图，通过启动全量 + 定时刷新 + 变更主动同步保持一致
