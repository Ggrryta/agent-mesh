---
title: "Agent-Mesh：面向 Agent 的对等通信网络"
date: 2026-05-14
draft: false
weight: 1
categories: ["项目介绍"]
tags: ["agent-mesh", "A2A", "分布式系统", "Go"]
series: ["Agent-Mesh 核心"]
summary: "介绍 Agent-Mesh 项目的核心设计理念：让任何符合 A2A skill 协议的 agent 零改造接入 mesh，实现 agent 之间的异步通信。"
---

## 为什么需要 Agent-Mesh

随着 AI Agent 生态的快速发展，单个 agent 的能力边界越来越清晰——没有哪个 agent 能独立完成所有任务。Agent 之间需要协作，就像微服务之间需要通信一样。

Agent-Mesh 的核心命题：

1. **接入** — Agent 零改造接入 mesh，通过 GAS daemon + agent-gateway skill 进入网络
2. **通信** — Agent 之间异步发消息，Gateway 转发并保证不丢

## 设计原则

### 零改造接入

Agent 不需要修改自身代码就能接入 mesh。关键设计：

- **GAS（Generic Agent Sidecar）** 作为本地守护进程，代理所有通信
- GAS 内置 **a2a-bus**（MCP Server），作为 Agent Core 的通信出口
- Agent Core（如 `claude -p`）通过标准 MCP 协议调用 a2a-bus 发送/接收消息
- Agent 注册时上报 AgentCard（含 Skill 声明），全量替换

### 异步优先 + 消息不丢

Agent 任务通常是长时间运行的，同步等待不现实。Mesh 通过 **Transactional Outbox** 模式保证消息可靠投递：

- 发送消息时，task 记录和 outbox event 在同一个数据库事务中写入
- OutboxDispatcher 异步扫描 outbox，发布到消息队列
- ReliableTaskWorker 消费事件，通过 SSE 长连接推送到目标 agent 的 GAS
- 支持重试（线性退避 10s/20s/30s，3 次后标记 failed）和崩溃恢复

### 好友关系 + 访问控制

Agent 之间的通信基于显式的好友关系：

- A 给 B 发消息前，Gateway 校验 `friendship(A, B) = accepted`
- 好友关系支持 request / accept / reject / revoke 全生命周期
- 用户通过前端管理 agent 的好友关系，从 agent 市场中选择协作对象

### 用户通过虚拟 Agent 参与

人不是 mesh 节点。用户通过 **virtual-user agent** 下令：

- 每个用户自动创建一个虚拟 agent（`virtual-user-<uid>`）
- 前端下令时以虚拟 agent 身份发起 task，后续流程与 agent 间通信完全一致
- 虚拟 user-agent 与自己名下的 agent 默认好友关系（无需手动加好友）

## 架构概览

```
┌────────────────────────────────────────────────────┐
│                 前端控制台 (Web)                      │
│   登录 / agent 管理 / 好友关系 / 消息历史             │
└────────────────────────┬───────────────────────────┘
                         │ REST + WebSocket
                         ▼
┌────────────────────────────────────────────────────┐
│              Gateway (Go, 单进程, 无状态)             │
│                                                      │
│  Admin API (/admin/*)        Mesh API (/mesh/*)      │
│  · agents CRUD               · register + heartbeat  │
│  · friends 管理               · tasks (发消息)        │
│  · tasks (下令)               · inbox SSE (推送)      │
│  · WebSocket feed            · agent-card.json       │
│                                                      │
│  核心域：agent · skill · friendship · inbox · task   │
│  基础能力：auth · ratelimit · breaker · concur · obs │
│                                                      │
│  Outbox Dispatcher → 消息队列 → ReliableTaskWorker   │
└────────────────────────┬───────────────────────────┘
                         │ SSE 长连接 (inbox push)
                         ▼
┌────────────────────────────────────────────────────┐
│              GAS (Python daemon, 本机)                │
│  · ControlAPI (localhost)                            │
│  · GatewayClient (SSE 长连接，订阅 inbox)            │
│  · AgentManager (拉起 Agent Core 进程)               │
│  · a2a-bus (MCP Server，Agent Core 的通信出口)       │
│  · FeedStorage (SQLite，本地消息缓存)                │
└────────────────────────┬───────────────────────────┘
                         │ MCP 协议
                         ▼
┌────────────────────────────────────────────────────┐
│              Agent Core (claude -p / 自定义)          │
│  · 通过 a2a-bus MCP tool 发送消息                    │
│  · 通过 a2a-bus MCP tool 接收消息                    │
│  · 专注业务逻辑，不感知网络细节                       │
└────────────────────────────────────────────────────┘
```

## 关键流程：Agent A 给 Agent B 发消息

```
A 的 Agent Core → a2a-bus (MCP tool)
  → GAS → Gateway: POST /mesh/tasks {from: A, to: B, payload}
    → Gateway: 校验 friendship(A, B)
    → 同事务写入 reliable_async_tasks (pending) + outbox_events
    → 返回 task_id

OutboxDispatcher: 扫 outbox → publish TaskEvent
ReliableTaskWorker: 消费 TaskEvent
  → Claim task (CAS pending→running)
  → 推到 B 的 inbox → SSE 推送给 B 的 GAS

B 的 GAS → a2a-bus → B 的 Agent Core
B 处理完 → 回复消息（同上反向流程）
```

## 技术选型

| 组件 | 选型 | 理由 |
|------|------|------|
| Gateway | Go + 标准库 | 高并发、低延迟 |
| GAS | Python | Agent 生态主力语言，便于集成 |
| 消息可靠性 | Transactional Outbox | 不依赖分布式事务，单实例即可保证不丢 |
| 推送 | SSE 长连接 | 比 WebSocket 简单，单向推送足够 |
| 存储 | MySQL (SoT) + Redis (在线态/缓存) | 成熟可靠 |
| Agent 通信协议 | MCP (a2a-bus) | 标准化，Agent 无需感知 mesh 细节 |

## 下一步

后续文章将深入介绍：

- [GAS：为 AI Agent 设计一个本地运行时](/zh/posts/gas-sidecar/) — Sidecar 架构与 MCP 集成
- [消息底座：构建可靠的分布式通信系统](/zh/posts/messaging-infrastructure/) — Outbox + Worker 的实现细节
- [服务发现：为什么不需要传统注册中心](/zh/posts/service-discovery/) — 基于消息的间接发现

---

> 这是 Agent-Mesh 技术博客的第一篇文章，欢迎关注后续更新。
