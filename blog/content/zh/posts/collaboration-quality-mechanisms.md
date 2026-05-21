---
title: "语义握手、行为标注、复杂度评估：三个协作质量机制的落地实现"
date: 2026-05-20
draft: false
categories: ["工程实践"]
tags: ["multi-agent", "信任机制", "语义握手", "行为标注", "协作质量"]
series: ["协作设计"]
summary: "从博客里的设计原则到代码落地——复杂度评估、语义握手协议、行为标注三个机制如何在 Agent Mesh 中实现，以及我们做出的工程取舍。"
---

> 本文是《多 Agent 协作的五个设计原则》的工程续篇。上一篇提出了语义分区、信任机制、协作光谱等理论框架；本文记录这些理论如何变成可运行的代码。

---

## 背景：从"全信"到"有据可查"

在实现多 Agent 协作的过程中，我们遇到了一个反复出现的问题：

```
用户："帮我写一个 HTTP server"
Alice 委派给 Bob
Bob 回复："写好了，编译通过，在 /tmp/server/main.go"

问题：
  - Bob 真的跑了 go build 吗？还是只是"觉得"能编译？
  - Alice 怎么判断这个回复可信？
  - 用户怎么知道这个结果经过了验证？
```

之前的模式是"全信"——收到就用，不验证。这在简单任务中可行，但在复杂协作中会导致错误累积：A 基于 B 的未验证结论做决策，C 又基于 A 的决策继续推进，最终发现源头就是错的。

我们需要三个机制来管理协作质量：

1. **复杂度评估**——决定一个任务需要多少"仪式感"
2. **语义握手**——执行前对齐理解，减少返工
3. **行为标注**——让每条回复的可信度可见

---

## 机制一：复杂度评估

### 设计决策

第一个问题是：谁来评估复杂度？

| 方案 | 优点 | 缺点 |
|------|------|------|
| LLM 评估 | 理解语义 | 贵、慢、不确定 |
| 发送方指定 | 零成本、最准确 | 依赖发送方判断 |
| 规则推断 | 确定性、零成本 | 粗糙 |

我们选了**发送方指定 + 规则兜底**的组合。

### 实现

`mesh_send_message` 和 `mesh_broadcast` 新增可选的 `complexity` 参数：

```typescript
mesh_send_message(
  to_agent_id: "bob-coder@example",
  text: "重构 task 模块的状态机逻辑，抽到独立文件",
  complexity: "high"
)
```

发送方不指定时，启发式规则自动推断：

```typescript
function inferComplexity(text: string): 'low' | 'medium' | 'high' {
  const highSignals = ['重构', '架构', '设计', '迁移', '从零', '全面', '系统性', '多模块']
  if (highSignals.some(s => text.includes(s))) return 'high'
  if (text.length > 200 || text.includes('步骤') || text.includes('然后')) return 'medium'
  return 'low'
}
```

复杂度通过消息的 metadata part 传递给接收方：

```typescript
parts: [
  { kind: 'text', text: '重构 task 模块...' },
  { kind: 'metadata', key: 'complexity', value: 'high' }
]
```

### 工程取舍

规则推断很粗糙——"重构"一定是 high 吗？"然后"一定意味着多步骤吗？不一定。但这个粗糙是有意为之的：

- 它是**兜底**，不是主力。大部分情况下发送方（Alice）会根据自己的判断显式指定
- 误判的代价很低——把 low 误判为 medium 只是多了一步确认，不会造成错误
- 规则可以随时调整，不需要重新训练模型

---

## 机制二：语义握手

### 设计决策

语义握手的核心问题是：**怎么让 agent 在执行前先确认理解？**

我们考虑了三种方案：

| 方案 | 可靠性 | 复杂度 |
|------|--------|--------|
| 纯 prompt 引导 | 低（LLM 可能跳过） | 低 |
| 状态机强制阻断 | 高（代码保证） | 高 |
| 双层组合 | 中高 | 中 |

纯 prompt 不够可靠——agent 可能直接跳过确认步骤开始执行。状态机强制阻断太复杂——需要判断"这条回复是确认理解还是执行结果"，这本身又需要 LLM。

我们选了**双层组合**：system prompt 教 agent 自主判断 + runtime 对标记了 complexity 的消息强制注入引导。

### 实现

**第一层：system prompt 里的自主判断**

所有 agent 共享的 meshSystemGuide 里写明：

```
语义握手（收到任务时的自主判断）：
  收到一个新任务时，先评估是否需要确认理解再执行：
  - 任务有歧义（"优化这个服务"——优化什么？延迟？内存？吞吐？）→ 先确认
  - 任务涉及多步骤或多文件 → 先确认方案
  - 任务有隐含假设（"部署到生产"——要不要先跑测试？）→ 先列出假设让对方确认
  - 任务很简单明确（"查一下版本号"）→ 直接执行，不需要确认
```

这层靠 agent 自己判断——即使发送方没标记 complexity，agent 也可能主动触发握手。

**第二层：runtime 注入的强引导**

当消息携带 `complexity=medium/high` 元数据时，`formatEventAsPrompt` 生成不同的引导语：

```typescript
if (complexity === 'high' || complexity === 'medium') {
  lines.push(`⚠️ 这是一个${complexity === 'high' ? '高' : '中等'}复杂度任务，请先确认你的理解再执行：`)
  lines.push(`1. 用 mesh_reply 回复你理解的范围、方法和假设`)
  lines.push(`2. 等对方确认后再动手执行`)
  lines.push(`3. 如果有疑问，在回复中列出`)
  lines.push(``)
  lines.push(`回复格式参考：`)
  lines.push(`  范围：...`)
  lines.push(`  方法：...`)
  lines.push(`  假设：...`)
  lines.push(`  疑问：...`)
}
```

### 实际效果

```
Alice → Bob（complexity=high）：
  "重构 task 模块，把状态机逻辑从 service.go 抽到独立的 fsm.go"

Bob 收到的 prompt：
  ⚠️ 这是一个高复杂度任务，请先确认你的理解再执行...

Bob 回复：
  范围：把 Transition()、IsAllowedTransition()、allowedTransitions map 抽到 fsm.go
  方法：新建 fsm.go，移动函数，service.go 改为调用 fsm 包
  假设：不改 Service 的公开 API，只是内部重组
  疑问：状态常量（StateSubmitted 等）也移过去吗？

Alice 确认：
  确认。状态常量也移过去，fsm.go 包含完整的状态机定义。

Bob 开始执行。
```

### 工程取舍

这不是硬性阻断——Bob 如果判断任务很清晰，可以跳过握手直接执行。我们接受这个"漏洞"，因为：

- 硬性阻断需要判断"回复是确认还是执行结果"——这个判断本身不可靠
- 大部分情况下 ⚠️ 标记 + 具体格式要求足以引导 agent 遵守
- 偶尔跳过的代价是"可能返工"，不是"系统崩溃"

---

## 机制三：行为标注

### 设计决策

博客里提出了"信任不能自证"的原则——不能让 agent 自己评估自己的可信度。那怎么标注？

我们的答案是：**不问 agent 它有没有验证，而是观察它实际做了什么，然后 runtime 独立确认。**

三层标注：

| 层级 | 标注 | 含义 | 可信度 |
|------|------|------|--------|
| 独立验证 ✓ | `[独立验证 ✓]` | runtime 自己确认了事实 | 最高 |
| agent 已验证 | `[agent 已验证]` | agent 调了验证工具（但 runtime 没独立确认） | 中 |
| 未验证 | `[未验证]` | 纯推理，无任何验证 | 低 |

### 实现

**第一层：prompt 引导（验证清单）**

```
验证清单（完成编码任务后必须执行）：
  □ 文件已创建/修改 → 用 Read 确认文件内容正确
  □ 编译通过 → 用 Bash 跑 go build 或 bun build
  □ 基本功能正确 → 用 Bash 跑 go test 或 go run
  注意：系统会自动检测你是否执行了验证，并在回复末尾追加标注。
```

最后一句"系统会自动检测"形成心理压力——agent 知道自己的行为会被审计。

**第二层：PostToolUse Hook（精确行为观察）**

通过 Claude SDK 的 `hooks.PostToolUse` 拦截工具执行结果，拿到完整的 exit code：

```typescript
hooks: {
  PostToolUse: [{
    hooks: [async (event) => {
      const toolName = event?.tool_name || ''
      const exitCode = event?.tool_result?.exit_code
      const cmd = event?.tool_input?.command || ''
      if (toolName === 'Bash' && exitCode === 0) {
        if (cmd.includes('go build')) turnCtx.verifications.push('编译通过(exit 0)')
        else if (cmd.includes('go test')) turnCtx.verifications.push('测试通过(exit 0)')
      } else if (toolName === 'Bash' && exitCode !== undefined && exitCode !== 0) {
        turnCtx.verifications.push(`命令失败(exit ${exitCode}): ${cmd.slice(0, 50)}`)
      }
      return { continue: true }
    }],
  }],
}
```

相比之前只看 `tool_use` 块（只知道"调没调"），PostToolUse hook 能区分"调了且成功"和"调了但失败"——标注精度大幅提升。

**第三层：事后独立验证**

`mesh_reply` 发送前，runtime 扫描回复文本中的可验证声明，自己确认：

```typescript
async function runIndependentVerification(replyText: string, cwd: string): Promise<string[]> {
  const results: string[] = []

  // 检测"创建了 xxx.go" → stat 确认文件存在
  const filePathPattern = /(?:创建|写入|生成).*?([/\w.\-@]+\.\w{1,5})/g
  // ... 对每个匹配的路径执行 stat

  // 检测"编译通过" → 跑 go build 确认
  if (replyText.includes('编译通过')) {
    await execAsync('go build ./...', { cwd, timeout: 15000 })
    results.push('编译通过')
  }

  // 检测"测试通过" → 跑 go test 确认
  if (replyText.includes('测试通过')) {
    await execAsync('go test ./...', { cwd, timeout: 30000 })
    results.push('测试通过')
  }

  return results
}
```

**标注组合逻辑**：

```typescript
const independentResults = await runIndependentVerification(args.text, turnCtx.cwd)
const agentVerified = turnCtx.verifications.length > 0

if (independentResults.length > 0) {
  annotations.push(`[独立验证 ✓] ${independentResults.join('、')}`)
}
if (agentVerified) {
  annotations.push(`[agent 已验证] ${remaining.join('、')}`)
}
if (annotations.length === 0) {
  annotations.push('[未验证] 以上结论基于推理，未执行验证命令')
}
```

### 实际效果

**Bob 写了代码并验证成功**：

```
hello.go 已创建，编译通过，可以直接 go run。

[独立验证 ✓] 文件存在: hello.go、编译通过
[agent 已验证] 编译通过(exit 0)、运行成功(exit 0)
```

**Bob 跑了命令但失败了**：

```
代码写好了，但编译有个小问题，我来修一下。

[agent 已验证] 命令失败(exit 1): go build ./...
```

**Bob 只是给了建议**：

```
建议把查询改成批量接口，预计能降低 60% 的延迟。

[未验证] 以上结论基于推理，未执行验证命令
```

### 工程取舍

1. **独立验证只做低成本操作**——stat 文件（<1ms）、go build（<5s）。不会跑完整的集成测试或性能测试。

2. **关键词匹配不完美**——"编译通过"的检测靠字符串匹配，agent 如果说"build 成功了"可能匹配不到。但这是可以持续补充的规则集。

3. **标注是强制追加的**——agent 无法删除或伪造标注。它写的 `text` 参数会被 runtime 在末尾拼接标注后再发送。这是"外部赋予信任"的具体体现。

---

## 三个机制的协同

```
用户发任务给 Alice
    │
    ▼
Alice 评估复杂度（显式指定或启发式推断）
    │
    ├── low → effort=low, thinking=disabled
    │         直接委派 Bob，Bob 执行后回复带行为标注
    │
    ├── medium → effort=medium, thinking=adaptive
    │            语义握手（Bob 先确认理解）→ 执行 → 行为标注
    │
    └── high → effort=high, thinking=adaptive
               语义握手 → 执行 → 行为标注 → Alice 自己也验证
                                                → 双重标注

全程硬性防护：maxTurns=30, maxBudgetUsd=$5
PostToolUse hook 实时记录每个工具调用的成功/失败
```

三个机制不是独立的——复杂度同时决定三件事：是否触发握手、推理深度多少、验证要求多严格。

---

## 与博客理论的对应

| 博客原则 | 落地实现 |
|---------|---------|
| 语义分区不能解决，只能管理 | 语义握手让隐含假设显式化 |
| 信任 = 锚点密度 | 行为标注的三级就是锚点密度的具象化 |
| 信任不能自证 | 标注基于行为观察 + 独立验证，不问 agent 自评 |
| 复杂度评估前置 | complexity 参数 + 启发式推断 |
| 纵深防御 | prompt 引导 + 行为观察 + 独立验证，三层叠加 |

---

## 已知局限和后续方向

1. **语义握手不是硬性阻断**——agent 可以跳过。后续可以加状态机：收到 high 任务后标记为 `awaiting_confirmation`，只有收到确认后才允许执行。

2. **独立验证只覆盖"文件存在"和"编译通过"**——更复杂的验证（逻辑正确性、性能达标）需要更多规则或引入专门的验证 agent。

3. **规则匹配是脆弱的**——"编译通过"的检测靠关键词，换个说法就匹配不到。后续可以用轻量 LLM 做声明提取，但会增加成本和延迟。

这些局限是有意识的工程取舍——先用低成本方案覆盖 80% 的场景，再根据实际数据决定哪些地方值得投入更多。

---

## 附：SDK 级别的资源管控

除了协作质量机制，我们还利用 Claude SDK 的原生能力做了资源管控：

### 推理深度随复杂度自适应

复杂度评估不只影响语义握手——它还直接控制 LLM 的推理深度：

```typescript
// low complexity → 快速响应，省 token
effort: 'low', thinking: { type: 'disabled' }

// medium complexity → 适度推理
effort: 'medium', thinking: { type: 'adaptive' }

// high complexity → 深度推理，质量优先
effort: 'high', thinking: { type: 'adaptive' }
```

简单任务（"查一下版本号"）关闭 extended thinking，响应快、成本低。复杂任务（"重构状态机"）开 adaptive thinking，让模型充分推理后再行动。

这是复杂度评估的第二个消费者——第一个是语义握手（决定要不要确认），第二个是推理深度（决定想多深）。

### 硬性防护

```typescript
maxTurns: 30,      // 单次事件最多 30 轮对话
maxBudgetUsd: 5.0, // 单次事件最多花 $5
```

之前靠环路检测 hint（prompt 引导 agent 关闭 task），agent 可能不遵守。现在是 SDK 级别的硬限制——到了就停。这是"纵深防御"的又一层：prompt 引导是软限制，SDK 参数是硬限制。

### PostToolUse Hook 替代流观察

早期版本通过观察 SDK 消息流中的 `tool_use` 块来记录 agent 行为——但只能看到"调了什么工具"，看不到"结果是什么"。

升级到 PostToolUse hook 后：

| 维度 | 之前（流观察） | 现在（PostToolUse hook） |
|------|--------------|----------------------|
| 能看到什么 | tool name + input | tool name + input + result（含 exit code） |
| 标注精度 | "编译验证"（不知成败） | "编译通过(exit 0)" 或 "命令失败(exit 1)" |
| 实现位置 | runtime 消息流循环 | SDK options.hooks |

这是一个典型的"用平台能力替代自建逻辑"的改进——SDK 原生支持的事情不需要自己 hack。

---

*本文记录的机制已在 Agent Mesh 中部署运行。代码位于 `meshd/src/tools/mesh-tools.ts`（复杂度评估 + 行为标注 + 独立验证）和 `meshd/src/agent/runtime.ts`（语义握手 + 验证清单 + SDK options 配置）。*
