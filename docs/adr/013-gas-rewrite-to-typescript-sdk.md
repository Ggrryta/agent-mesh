# ADR 013: GAS 重写为 TypeScript + Claude Agent SDK

**Status**: Accepted
**Date**: 2026-05-14

## Context

初版 GAS（Go）定位为"被 Claude Code 拉起的 MCP server"：
- Claude Code 作为 agent 主体（推理大脑）
- GAS 作为 MCP server 提供 mesh tools（send / reply / inbox pull）
- 用户在 Claude Code 里输入指令时，Claude 调 mesh tools 与队友协作

实际验证后发现这个架构**无法支撑核心需求**"agent 自主协作"：

1. Claude Code 是会话驱动的被动模型 —— 没人输入就不动
2. GAS 作为 MCP subprocess 只能响应 Claude 的 tool 调用，无法反向"唤醒" Claude
3. MCP 协议的 notifications 机制在 Claude Code 中当前**不会唤醒 model**（GitHub issue #38027），只在下次交互时才能看到
4. inbox 事件到达时无法触发 LLM 推理，协作链中断

## Decision

**放弃"Claude Code + GAS as MCP server"模型**，改为 **Claude Agent SDK + TypeScript** 实现 GAS 作为完整的 agent runtime。

新架构：

```
GAS daemon（TypeScript，基于 Claude Agent SDK）
  ├─ 启动：API Key → JWT，拉 GET /mesh/agents/me 拿 system_prompt
  ├─ Inbox 长轮询：wait=20s 拿事件
  ├─ 事件到达 → 格式化成自然语言 → 调 SDK query()
  ├─ SDK 带 system_prompt + mesh tools 推理
  ├─ LLM 决策 → 调 mesh tool（send / reply / get_roster / get_timeline / set_status）
  └─ Tool 执行 → HTTP 到 Gateway
```

一个 GAS 进程 = 一个常驻 agent。GAS 完全脱离 Claude Code 桌面客户端。

## Alternatives Considered

### 1. 继续用 Claude Code + Channels
等 Anthropic 修复 issue #38027（notifications 唤醒 model）。**否决**：依赖上游不可控，时间线不明。

### 2. 自研 agent runtime
自己写 prompt 管理、context 管理、tool 调用循环。**否决**：工作量 3-6 人月，质量不如 SDK。

### 3. 用 LangChain / LlamaIndex
**否决**：LangChain 的 agent 抽象过于泛化，tool 调用细节暴露得少；LlamaIndex 更偏向 RAG 不是 agent。SDK 是 Anthropic 官方针对 Claude 优化的，最贴合需求。

### 4. Python SDK 而非 TypeScript
**否决**：
- 前端已经是 TypeScript，语言统一减少认知负担
- Bun 可编译独立 binary，分发体验接近 Go
- 强类型在长期运行的 daemon 上更稳

### 5. 保留 Go GAS，通过 IPC 调 Python/TS SDK runner
**否决**：一个功能两个进程，故障面翻倍，维护成本高。

## Consequences

### 正面

- **核心需求可达成**：agent 收到 inbox 事件时能真正触发 LLM 推理
- **工作量可控**：SDK 处理了 prompt / context / tool parsing，我们只写 ~500 行业务代码
- **跟 SDK 升级同步**：Anthropic 推新 feature（如 prompt caching、streaming tool use）我们直接受益
- **language alignment**：跟前端统一 TypeScript，未来可共享 schema / types

### 负面

- **多一个语言栈**：Gateway（Go）+ 前端（TS）+ GAS（TS）三栈变两栈的愿景暂时实现不了，除非 Gateway 也换
- **Anthropic 锁定**：SDK 只支持 Claude。如果未来要支持 GPT / Gemini，得写 provider adapter 或另起一套
- **Node 运行时依赖**：部署需要 Node.js（或 Bun 编译出的 binary）
- **Go GAS 代码作废**：~800 行代码成为参考实现，不再维护

### 迁移影响

- `gas/` 目录保留，加 DEPRECATED.md，不再开发
- `gas-ts/` 为新实现
- Gateway API 完全兼容，不需要变更
- 用户工作流简单：设置环境变量（含 `ANTHROPIC_API_KEY`）启动 `gas-ts/dist/gasd`

## Open Questions

1. **如何处理 SDK 调用成本**？每个 inbox 事件都触发一次 LLM 调用，成本可能快速累积。MVP 先不优化，观察实际用量后再加 batching / dedup。
2. **session 是否跨 event 保持**？当前实现是每个 inbox 事件起一个新 query（无跨 event 上下文）。后续可能要改成"长 session per collaboration context"。
3. **如何支持非 Claude 模型**？暂时不支持，SDK 锁定 Anthropic。长期愿景可能引入抽象层。
