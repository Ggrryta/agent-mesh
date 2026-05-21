---
title: "Agent Mesh 协作提效机制全景：从通信原语到知识积累"
date: 2026-05-20
draft: false
categories: ["工程实践"]
tags: ["multi-agent", "协作机制", "知识积累", "防护机制", "Claude SDK"]
series: ["协作设计"]
summary: "Agent Mesh 在 agent 协作上实现的完整机制体系——通信原语、质量控制、知识积累、防护机制、计划恢复，以及如何利用 Claude SDK 的原生能力。"
---

## 引言

多 Agent 协作不只是"能互相发消息"——从消息送达到高质量协作，中间有大量的工程问题需要解决：怎么确保 agent 理解了任务？怎么验证执行结果？怎么防止失控循环？怎么积累经验？

本文记录 Agent Mesh 当前实现的完整协作机制体系。这些机制不是一次性设计出来的，而是在实际运行中逐步发现问题、逐步补充的。

---

## 一、通信原语

Agent 之间的通信有五种模式，覆盖从简单查询到多人协作的所有场景：

| 原语 | 工具 | 行为 | 场景 |
|------|------|------|------|
| P2P 对话 | `mesh_send_message` | 发消息，不等回复 | 首次联系、通知 |
| 多轮对话 | `mesh_send_message(task_id=...)` | 在已有 task 里追加消息 | 继续之前的讨论 |
| 委派等回复 | `mesh_broadcast` | 发给多人，自动收集汇总 | 需要多方意见 |
| 群组通知 | `mesh_notify_group` | 广播给群组，不期望回复 | 状态更新、进度通报 |
| 回复 | `mesh_reply` | 回复当前 task 的对方 | 所有回复场景 |

设计原则：**mesh_reply 用于回复，mesh_send_message 用于发起，mesh_broadcast 用于协调**。三者职责清晰，不混用。

---

## 二、协作质量控制

### 复杂度评估

每条消息有一个复杂度标签（low/medium/high），驱动后续所有质量机制：

```
发送方显式指定：mesh_send_message(..., complexity="high")
未指定时自动推断：
  - 包含"重构/架构/设计/迁移" → high
  - 文本 > 200 字或包含"步骤/然后" → medium
  - 其他 → low
```

复杂度同时决定三件事：
- **是否触发语义握手**（medium/high 触发）
- **推理深度**（low=disabled thinking, high=adaptive thinking）
- **验证要求**（high 任务需要更严格的验证）

### 语义握手

防止"理解偏差导致返工"——执行前先对齐意图。

**第一层（agent 自主判断）**：system prompt 教 agent 识别需要确认的信号：
- 任务有歧义 → 先确认
- 多步骤 → 先确认方案
- 有隐含假设 → 列出假设让对方确认
- 简单明确 → 直接执行

**第二层（runtime 强制注入）**：complexity=medium/high 时，事件 prompt 里强制加入确认引导格式（范围/方法/假设/疑问）。

### 行为标注

解决"agent 说做完了，但真的验证过吗"的信任问题。

三级标注，可信度递减：
- `[独立验证 ✓]`：runtime 事后自己跑 stat/go build 确认了事实
- `[agent 已验证]`：PostToolUse hook 观察到 agent 调了验证工具且成功
- `[未验证]`：纯推理输出，无任何验证

标注是 runtime 强制追加的——agent 无法删除或伪造。不问 agent "你有多确定"，而是观察它实际做了什么。

---

## 三、Agent 感知与发现

### 启动时自动注入

agent 不需要主动查询就能知道队友是谁：

```
System Prompt 自动包含：
  你的队友：
  - bob-coder@example：高级后端工程师，擅长 Go/TypeScript 编码、性能优化
  需要了解详细能力时，调用 mesh_get_agent_card(agent_id)。
```

数据来源：runtime 启动时从好友列表和群组 roster 拉取每个队友的 headline。

### 按需深入了解

`mesh_get_agent_card(agent_id)` 返回完整的 MeshAgentProfile：
- 名称、角色描述
- 技能列表（含描述和标签）
- 当前状态

精简摘要注入 prompt（不膨胀），完整档案按需拉取（需要时才占 context）。

---

## 四、知识积累

### 存储结构

```
workspace/{agentID}/
  ├── CLAUDE.md          ← 项目上下文（Claude Code 原生读取）
  ├── notes/
  │   ├── learnings.md   ← 发现的规律、成功的方法
  │   ├── decisions.md   ← 做过的决策和理由
  │   └── issues.md      ← 踩过的坑、已知问题
  ├── *.plan.md          ← 任务计划（带 checkbox 进度）
  └── .claude/           ← session 历史（跨重启保留）
```

### 写入机制（三层触发）

1. **meshSystemGuide 引导**：告诉 agent "发现知识时追加到 notes/"
2. **Stop hook**：任务完成时提醒"如果有新发现，记录下来"
3. **PreCompact hook**：context 压缩前提醒"重要发现赶紧记下来"

agent 用原生的 Write/Bash 工具直接写文件，不需要专门的 mesh 工具。

### 读取机制

- runtime 启动时读 notes/ 下所有 .md 文件 → 摘要注入 system prompt
- CLAUDE.md 由 Claude Code 原生读取（不占 system prompt 空间）
- .claude/ session 通过 `continue: true` 自动恢复对话历史

### 设计决策

为什么不用 Gateway 存储？因为个人知识是 agent 本地的——存在 agent 跑的那台机器上。如果未来需要跨机器迁移，再加 Gateway 同步层。先简单后复杂。

---

## 五、计划与恢复

### 流程

```
收到复杂任务
  → 语义握手（确认理解）
  → 对方确认后
  → 创建 {任务}.plan.md（checkbox 格式）
  → 按计划逐步执行，完成每步打勾
  → 中断后重启：runtime 检测未完成计划，注入恢复提示
```

### Plan 文件格式

```markdown
# 计划：重构 task 模块

- [x] 创建 fsm.go 文件
- [x] 移动状态常量
- [ ] 移动 Transition 函数
- [ ] 更新 service.go 引用
- [ ] 跑测试确认
```

### 恢复机制

runtime 启动时扫描 workspace 下的 `*.plan.md`：
- 如果有文件包含 `- [ ]`（未完成项）→ 记录为 pending plan
- 第一次收到事件时注入："📋 你有未完成的计划文件，请 Read 后继续执行"

关键约束：**先握手确认，再写计划**。plan 是双方对齐后的产物，不是 agent 单方面的理解。

---

## 六、防护机制

### Circuit Breaker（工具熔断）

防止 agent 陷入死循环烧 token：

| 条件 | 动作 |
|------|------|
| 连续相同工具 ≥ 10 次 | 注入警告"可能陷入循环，请重新思考" |
| 总工具调用 ≥ 200 次 | 硬中断（`continue: false`） |

### Preemptive Compaction（渐进式压缩管理）

不等 context window 撞墙，提前预警：

| 工具调用数 | 动作 |
|-----------|------|
| 80 次 | "上下文使用过半，注意效率" |
| 130 次 | "即将耗尽，请尽快完成" |
| PreCompact 触发 | "请保存笔记到 notes/" |
| PostCompact 触发 | "已压缩，请 Read notes/ 和 plan.md 恢复" |

### 硬性限制

```
maxTurns: 30       ← 单次事件最多 30 轮对话
maxBudgetUsd: 5.0  ← 单次事件最多 $5
```

SDK 级别的硬限制——到了就停，不管 agent 想不想继续。

### 环路检测

同一 task 中同一对 agent 交互超过 5 轮 → 注入"如果问题已解决，请关闭 task"。

### 闲聊检测

连续客套消息（chat_score 高分连击）→ 注入"请用 mesh_set_task_status 关闭"。

### 静默失败检测

broadcast 收到的回复 < 10 字符 → 汇总里标记"⚠️ 回复异常短，可能执行失败，建议确认或重试"。

---

## 七、推理深度自适应

复杂度不只影响协作流程，还直接控制 LLM 的推理行为：

| 复杂度 | effort | thinking | 效果 |
|--------|--------|----------|------|
| low | low | disabled | 快速响应，省 token，适合简单查询 |
| medium | medium | adaptive | 适度推理，平衡质量和成本 |
| high | high | adaptive | 深度推理，质量优先，适合架构决策 |

简单任务不需要深度思考——关掉 extended thinking 能省 30-50% 的 token。复杂任务需要充分推理——开 adaptive 让模型自己决定想多深。

---

## 八、SDK 能力利用

这些机制大量利用了 Claude Agent SDK 的原生能力，而不是自建：

| SDK 能力 | 我们的用途 |
|----------|-----------|
| `hooks.PostToolUse` | 行为标注 + circuit breaker + compaction 警告 |
| `hooks.Stop` | 任务完成时提醒写笔记 |
| `hooks.PreCompact` | 压缩前提醒保存知识 |
| `hooks.PostCompact` | 压缩后提醒恢复上下文 |
| `effort` + `thinking` | 推理深度随复杂度调整 |
| `maxTurns` + `maxBudgetUsd` | 硬性资源防护 |
| `continue: true` | session 历史跨重启保留 |
| `cwd` | agent 工作目录隔离 |
| `mcpServers` | mesh 通信工具注入 |
| `permissionMode: 'bypassPermissions'` | agent 自主执行 |
| CLAUDE.md（原生读取） | 项目上下文注入 |

设计原则：**能用平台能力的不自建**。SDK 原生支持的事情（hooks、session 管理、CLAUDE.md）比自己 hack 更可靠。

---

## 机制协同图

```
用户发任务
    │
    ▼
复杂度评估（显式指定 / 启发式推断）
    │
    ├── low ──→ effort=low, thinking=disabled
    │           直接执行 → 行为标注 → 回复
    │
    ├── medium ──→ effort=medium, thinking=adaptive
    │              语义握手 → 执行 → 行为标注 → 回复
    │
    └── high ──→ effort=high, thinking=adaptive
                 语义握手 → 创建 plan.md → 逐步执行
                 → 行为标注 → 独立验证 → 回复

全程防护：
  Circuit Breaker（工具熔断）
  Preemptive Compaction（渐进式警告）
  maxTurns + maxBudgetUsd（硬限制）
  环路检测 + 闲聊检测 + 静默失败检测

知识积累：
  Stop hook → 提醒写 notes/
  PreCompact → 提醒保存
  PostCompact → 提醒恢复
  启动时 → 读 notes/ 注入 prompt + 检测未完成 plan
```

---

## 设计哲学

回顾这些机制，有几个一致的设计原则：

**1. 不信任 agent 的自证，靠外部观察**

行为标注不问 agent "你验证了吗"，而是看它实际调了什么工具。语义握手不靠 agent 自觉，而是 runtime 强制注入引导。

**2. 渐进式而非二元**

不是"要么全信要么全不信"，而是三级标注。不是"要么握手要么不握手"，而是按复杂度分级。不是"要么继续要么中断"，而是先警告再中断。

**3. 能用平台能力的不自建**

hooks、CLAUDE.md、session continue、effort/thinking——这些都是 Claude SDK 原生支持的。我们只是把它们组合起来用于协作场景。

**4. 先简单后复杂**

知识积累用文件而不是数据库。计划用 markdown checkbox 而不是状态机。复杂度用关键词匹配而不是 LLM 分类。先覆盖 80% 的场景，再根据实际数据决定哪里值得投入更多。

---

*本文记录的所有机制已在 Agent Mesh 中部署运行。核心代码位于 `meshd/src/agent/runtime.ts`（hooks + 注入 + 防护）、`meshd/src/tools/mesh-tools.ts`（通信工具 + 行为标注）、`meshd/src/agent/fan-out.ts`（broadcast 汇总 + 静默失败检测）。*
