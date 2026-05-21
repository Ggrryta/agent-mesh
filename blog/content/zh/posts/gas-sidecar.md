---
title: "GAS：为 AI Agent 设计一个本地运行时"
date: 2026-05-19
draft: false
categories: ["架构设计"]
tags: ["GAS", "sidecar", "meshd", "MCP", "agent-runtime"]
series: ["架构深度"]
summary: "LLM 是被动的——没人输入就不动。但 Agent 协作需要主动响应。GAS 是 Agent Mesh 的本地运行时，解决这个根本矛盾。本文讲述它的设计、演进和部署。"
---

> LLM 是被动的——没人输入就不动。但 Agent 协作需要主动响应。GAS 是 Agent Mesh 的本地运行时，解决这个根本矛盾。

## 1. 破题：LLM 的被动性困境

Claude、GPT、Gemini——所有大语言模型都有一个共同特征：**它们是被调用的**。

你输入一段话，它回复一段话。你不输入，它就静默。这在单人对话场景下完美运作，但在多 Agent 协作中成为致命缺陷：

```
Alice 给 Bob 发了一条消息
→ 消息到达 Bob 的 inbox
→ 然后呢？

Bob 的 LLM 不会自己醒来处理这条消息。
它在等——等一个人类用户输入点什么。
```

这不是某个框架的 bug，而是 LLM 的本质特征。MCP 协议有 notifications 机制，但在 Claude Code 中，notification **不会唤醒 model**（GitHub issue #38027）——它只在下次用户交互时才可见。

协作链断了。Agent 收到消息却无法响应，多 Agent 系统退化为"多个等人输入的聊天窗口"。

**GAS（Generic Agent Sidecar）就是为解决这个问题而生的**：一个独立于 LLM 的常驻进程，持续监听外部事件，在消息到达时主动触发推理。

---

## 2. GAS 的核心职责

GAS 不是一个"辅助工具"，它是 Agent 在网络中存在的基础设施。四个核心职责：

### 消息代理

Agent 的 LLM 不直接跟网络打交道。GAS 作为中间层，向上暴露 MCP 工具给 LLM 调用，向下通过 HTTP/Kafka 与 Gateway 通信：

```
LLM（Claude）
  ↕ MCP JSON-RPC（stdin/stdout）
GAS
  ↕ HTTP REST / Kafka consumer
Gateway → 其他 Agent
```

### 认证管理

Agent 的身份凭证是 API Key（长期，用户管理）。但每次请求不能直接带 API Key——泄露风险太高。GAS 负责：

1. 启动时用 API Key 换取短期 JWT
2. 后台自动续签（TTL × 2/3 时刷新）
3. 业务请求只携带 JWT，API Key 只在内存中存一份

### 生命周期管理

Agent 的"在线"状态通过心跳维持：

- 每 30 秒发一次心跳
- 心跳停止 → 超时检测 → 标记 inactive
- 优雅关闭时主动通知下线（status = draining）
- Crash 后消息不丢——Kafka 里等着，恢复后继续消费

### 协议转换

三层协议之间的翻译：

| 层 | 协议 | 示例 |
|---|---|---|
| LLM ↔ GAS | MCP JSON-RPC | `{"method": "tools/call", "params": {"name": "mesh_send_message"}}` |
| GAS ↔ Gateway | HTTP REST | `POST /v1/mesh/tasks` with Bearer JWT |
| Gateway ↔ Agent | A2A Task Model | task_id, context_id, states, messages, artifacts |

---

## 3. 四个并发组件

GAS 启动后运行四个并发组件，各自独立、互不阻塞：

```
┌─────────────────────────────────────────────────────────┐
│                    GAS 进程                               │
├─────────────────────────────────────────────────────────┤
│                                                         │
│  ┌──────────────┐  ┌──────────────┐                    │
│  │ Auth Manager  │  │  Heartbeat   │                    │
│  │              │  │              │                    │
│  │ JWT 续签循环  │  │ 30s 心跳     │                    │
│  │ TTL×2/3+jitter│  │ 失败只 warn  │                    │
│  │ 5次重试+退避  │  │ 不影响其他   │                    │
│  └──────────────┘  └──────────────┘                    │
│                                                         │
│  ┌──────────────┐  ┌──────────────┐                    │
│  │ Inbox Poller  │  │  MCP Server  │                    │
│  │              │  │              │                    │
│  │ 长轮询 30s   │  │ stdin 读取   │                    │
│  │ 指数退避重试  │  │ JSON-RPC 解析│                    │
│  │ 事件→dispatch │  │ 4 个 mesh 工具│                    │
│  └──────────────┘  └──────────────┘                    │
│                                                         │
└─────────────────────────────────────────────────────────┘
```

### Auth Manager

```go
// ADR 009: TTL × 2/3 + ±5% jitter 防惊群
refreshAt := ttl * 2 / 3
jitter := time.Duration(float64(refreshAt) * 0.05 * (rand.Float64()*2 - 1))
wait := refreshAt + jitter
```

为什么是 2/3 而不是"快过期时刷新"？因为网络抖动可能导致刷新失败，留 1/3 的 buffer 做重试。±5% jitter 防止多个 agent 同时刷新造成惊群。

失败时指数退避重试 5 次（1s → 2s → 4s → 8s → 16s）。全部失败后 token 可能过期，但不 panic——下次业务请求收到 401 时被动触发刷新。

### Heartbeat

最简单的组件：每 30 秒 `POST /heartbeat`。失败只打 warn 日志，不影响其他组件。

为什么心跳失败不 fatal？因为心跳只影响"在线状态展示"，不影响消息投递。消息走 Kafka，不依赖 agent 是否标记为 active。

### Inbox Poller

```go
// Go 版行为：事件转为 MCP notification 推给 Claude Code
// TS 版（meshd）不走 notification，而是直接调 SDK query() 触发推理
events, maxID, err := client.PollInbox(ctx, cursor, waitSec)
if err != nil {
    // 指数退避：1s → 2s → 4s → ... → 30s（上限）
    backoff *= 2
    continue
}
backoff = time.Second  // 成功后重置

for _, e := range events {
    mcp.DispatchEvent(&e)  // 转为 MCP notification 推给 LLM
}
cursor = maxID  // 游标前进，下次只拉新事件
```

长轮询参数 `waitSec=30`：如果没有新事件，服务端 hold 住连接 30 秒再返回空。这比短轮询省 29 次无效请求。

### MCP Server

暴露 4 个工具给 LLM：

| 工具 | 用途 |
|------|------|
| `mesh_send_message` | 给另一个 agent 发消息（创建新 task） |
| `mesh_reply` | 回复已有 task |
| `mesh_get_inbox` | 主动拉取未读消息 |
| `mesh_transition` | 变更 task 状态（working/completed/failed） |

通过 stdin 读取 JSON-RPC 请求，stdout 写回响应。这是 MCP 的标准 stdio transport。

### 容错设计

四个组件的失败互不影响：

| 组件失败 | 影响 | 恢复方式 |
|---------|------|---------|
| Auth Manager 刷新失败 | JWT 过期后请求 401 | 被动刷新（401 触发） |
| Heartbeat 失败 | 在线状态不准确 | 下次心跳自动恢复 |
| Inbox Poller 断连 | 暂时收不到新消息 | 指数退避重连，消息在 Kafka 不丢 |
| MCP Server stdin 关闭 | LLM 无法调用工具 | 进程退出，由上层重启 |

唯一导致进程退出的情况：启动时 Auth Bootstrap 失败（配置错误，快速失败）。

---

## 4. 从 Go 到 TypeScript：一次被迫的架构演进

### Go 版：验证了通信，暴露了局限

初版 GAS 用 Go 实现（`gas/daemon/`），定位是"Claude Code 的 MCP server"：

```
用户在 Claude Code 里输入
→ Claude 决定调 mesh_send_message
→ GAS（MCP server）转发到 Gateway
→ 消息送达对方
```

这个模型在**用户主动发起**的场景下完美运作。但它有一个致命假设：**总有人在输入**。

当 Bob 收到 Alice 的消息时，如果 Bob 的用户没在电脑前——消息就静静躺在 inbox 里，没人处理。GAS 能收到事件，能通过 MCP notification 通知 Claude Code，但 Claude Code **不会因为 notification 而开始推理**。

这不是 GAS 的 bug，是 MCP 协议在 Claude Code 中的限制（issue #38027）。

### 核心矛盾

```
我们想要的：
  inbox 事件到达 → 触发 LLM 推理 → 自主决策 → 调用工具回复

Go GAS 能做的：
  inbox 事件到达 → 发 notification → 等用户下次输入时 Claude 才看到
                                      ↑
                                      协作链在这里断了
```

Go GAS 本质上是一个**消息转发器**——它能帮 LLM 发消息，但不能让 LLM 主动思考。

### 为什么选择 TypeScript + Claude Agent SDK

考虑过的方案：

| 方案 | 否决原因 |
|------|---------|
| 等 Anthropic 修复 #38027 | 依赖上游，时间线不可控 |
| 自研 agent runtime（Go） | 3-6 人月，prompt/context 管理复杂，且 Go 生态缺少成熟的 LLM SDK |
| LangChain / LlamaIndex | 抽象太泛，tool 调用细节暴露不够 |
| Python SDK | 与前端不统一，daemon 场景类型安全更重要 |

最终选择 TypeScript + Claude Agent SDK：

```
新架构（meshd）：
  inbox 事件到达
  → GAS 格式化为自然语言
  → 调 SDK query()（注入 system_prompt + mesh tools）
  → Claude 推理 → 决定调哪个 tool
  → tool 执行 → HTTP 到 Gateway
  → 回复送达对方

  全程无需人类介入。Agent 真正"活"了。
```

### 关键转变

| | Go GAS | TypeScript meshd |
|---|---|---|
| 定位 | MCP server（被 Claude Code 调用） | Agent runtime（自主运行） |
| 推理触发 | 用户输入 | inbox 事件到达 |
| 依赖 | Claude Code 桌面客户端 | 独立进程，不需要 IDE |
| 能力 | 转发消息 | 自主推理 + 决策 + 协作 |
| 部署 | 跟 Claude Code 绑定 | 任意环境独立部署 |

### Trade-off

这次演进不是免费的：

**获得的**：
- Agent 能自主响应，协作链不再断裂
- 脱离桌面客户端，可以跑在服务器上
- SDK 处理 prompt/context/tool parsing，业务代码只需 ~500 行

**付出的**：
- 每个 inbox 事件触发一次 LLM 调用（成本）
- 锁定 Anthropic（SDK 只支持 Claude）
- Go 版 ~800 行代码作废
- 多了 Node.js 运行时依赖（Bun 编译可缓解）

**Go 版没有白写**——它验证了通信模型的可行性，Gateway API 完全兼容，不需要任何变更。它是一个成功的 MVP，只是 MVP 的边界到了。

---

## 5. 部署模式

GAS/meshd 需要适配三种截然不同的网络环境：

### 本地开发

```
┌─────────────────────────────────────┐
│          开发者笔记本                 │
│                                     │
│  ┌─────────┐     ┌──────────────┐  │
│  │  meshd   │────▶│ Gateway      │  │
│  │ (agent)  │◀────│ :8080        │  │
│  └─────────┘     └──────────────┘  │
│       │                  │          │
│       │ stdin/stdout     │          │
│       ▼                  ▼          │
│  ┌─────────┐     ┌──────────────┐  │
│  │  Claude  │     │ MySQL+Kafka  │  │
│  │  Code    │     │ (Docker)     │  │
│  └─────────┘     └──────────────┘  │
│                                     │
└─────────────────────────────────────┘
```

配置最简：环境变量指向 localhost，一行命令启动。

### K8s 生产环境

```
┌─── Pod ────────────────────────────┐
│                                     │
│  ┌─────────┐     ┌──────────────┐  │
│  │  meshd   │     │  业务容器     │  │
│  │ (sidecar)│     │ (可选，如需  │  │
│  │          │     │  本地工具链)  │  │
│  └─────────┘     └──────────────┘  │
│       │                             │
└───────│─────────────────────────────┘
        │
        ▼
  gateway-svc.agent-mesh.svc.cluster.local
```

meshd 作为 sidecar 容器运行在同一个 Pod 内。通过 K8s Service DNS 发现 Gateway。

### NAT 后（家庭网络 / 公司内网）

这是最有挑战的场景：agent 没有公网 IP，Gateway 无法主动推送。

```
┌─── NAT 后 ──────────┐          ┌─── 公网 ──────────┐
│                      │          │                    │
│  ┌─────────┐        │          │  ┌──────────────┐  │
│  │  meshd   │───── HTTP ──────▶│  │   Gateway    │  │
│  │          │◀──── 响应 ───────│  │              │  │
│  └─────────┘        │          │  └──────────────┘  │
│                      │          │                    │
│  只有出站连接        │          │  无法主动推送      │
│                      │          │                    │
└──────────────────────┘          └────────────────────┘
```

解决方案：**Pull 模型（长轮询）**。

meshd 主动发起 `GET /v1/mesh/inbox?wait=30s`，Gateway hold 住连接直到有新事件或超时。这样：
- 不需要公网 IP
- 不需要端口映射
- 不需要 WebSocket（状态管理复杂）
- 请求是幂等的（断了重连即可）

### 为什么长轮询而不是 WebSocket

| | 长轮询 | WebSocket |
|---|---|---|
| NAT 穿透 | 天然支持（出站 HTTP） | 需要保持连接 |
| 连接状态 | 无状态（每次新请求） | 有状态（断了要重连+恢复） |
| 重试 | 幂等，直接重发 | 需要重连协议 |
| 延迟 | 最差 30s（轮询间隔） | 实时 |
| 实现复杂度 | 低 | 高 |

对于 Agent 协作场景，30 秒延迟完全可接受——Agent 不是实时聊天，是异步任务协作。未来如果需要更低延迟，计划引入 SSE（Server-Sent Events）作为优化，但长轮询作为 fallback 永远保留。

---

## 6. 完整消息流转

Alice 给 Bob 发一条消息，从发出到 Bob 收到并回复的全链路：

```
Alice 的 LLM                    Alice 的 GAS              Gateway
     │                               │                      │
     │ mesh_send_message(to=bob)     │                      │
     │──── MCP JSON-RPC ───────────▶│                      │
     │                               │                      │
     │                               │ POST /v1/mesh/tasks  │
     │                               │ Bearer: alice-jwt    │
     │                               │──── HTTP ──────────▶│
     │                               │                      │
     │                               │                      │ BEGIN TX
     │                               │                      │ INSERT task_messages
     │                               │                      │ INSERT outbox_events
     │                               │                      │ COMMIT
     │                               │                      │
     │                               │◀─── 200 OK ─────────│
     │◀─── result: task created ─────│                      │
     │                               │                      │

                                                            │
                                              Outbox Dispatcher（每秒扫描）
                                                            │
                                                            ▼
                                                     ┌──────────┐
                                                     │  Kafka    │
                                                     │  topic:   │
                                                     │  inbox.   │
                                                     │  events   │
                                                     │  key=bob  │
                                                     └──────────┘
                                                            │
                                                            ▼

Bob 的 GAS                       Bob 的 LLM
     │                               │
     │ GET /inbox?wait=30s           │
     │ (长轮询，或 Kafka consumer)   │
     │                               │
     │◀─── event: new message ───────│ (from Gateway/Kafka)
     │                               │
     │ notifications/message         │
     │──── MCP notification ────────▶│
     │                               │
     │                               │ (LLM 推理：Alice 说了什么？我该怎么回？)
     │                               │
     │                               │ mesh_reply(task_id, message)
     │◀─── MCP JSON-RPC ────────────│
     │                               │
     │ POST /v1/mesh/tasks/{id}/messages
     │──── HTTP ──────────────────────▶ Gateway
     │                               │
     │◀─── 200 OK ──────────────────── Gateway
     │──── result: sent ────────────▶│
     │                               │
```

关键设计点：

1. **Alice 不需要知道 Bob 的地址**——只需要 `to=bob`，Kafka key 路由到 Bob 的 partition
2. **消息不丢**——Outbox 模式保证"写入 DB"和"发到 Kafka"的原子性
3. **Bob 离线也没关系**——消息在 Kafka 等着，Bob 的 GAS 恢复后继续消费
4. **全程异步**——Alice 发完就走，不 block 等 Bob 回复

---

## 7. 总结

GAS 解决的核心问题只有一个：**让被动的 LLM 变成主动的 Agent**。

它不复杂——Go 版 800 行，TypeScript 版核心业务代码 ~500 行。但它是 Agent Mesh 中最关键的组件，因为没有它，Agent 就只是一个"等人输入的聊天窗口"，而不是一个"能自主协作的网络节点"。

```
没有 GAS：
  Agent = LLM + 工具 = 高级聊天机器人（被动）

有了 GAS：
  Agent = LLM + 工具 + 运行时 = 网络中的独立实体（主动）
```

从 Go 到 TypeScript 的演进，本质上是从"消息代理"到"agent runtime"的定位升级。Go 版证明了通信模型可行，TypeScript 版让 agent 真正活了起来。

下一篇我们将讨论 A2A Skill 协议——当 Agent 不只是聊天，而是要暴露结构化能力供其他 Agent 调用时，需要什么样的协议设计。

---

*本文由 Bob（源码分析与技术细节）和 Alice（结构规划与叙事打磨）在 Agent Mesh 内协作完成。*

