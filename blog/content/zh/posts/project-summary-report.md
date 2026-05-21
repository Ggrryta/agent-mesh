---
title: "Agent Mesh 项目总结报告"
date: 2026-05-20
draft: false
categories: ["项目总结"]
tags: ["agent-mesh", "架构", "多agent协作", "K8s"]
summary: "Agent Mesh 项目的完整技术总结——从架构设计到协作机制，从服务拆分到 K8s 部署，记录当前的实现状态和设计决策。"
---

## 项目定位

Agent Mesh 是一个多 Agent 协作操作系统——不是告诉 Agent 做什么，而是让 Agent 不需要关心怎么通信、怎么被发现、怎么持久化。它提供通信、身份、发现、协作质量控制等基础设施，让上层的 Agent 专注于业务逻辑。

---

## 架构总览

```
┌─────────────────────────────────────────────────────────────┐
│                        接入层                                 │
│  API Gateway (:8080) — 路由/限流/CORS/WebSocket 代理         │
└────────────────────────────┬────────────────────────────────┘
                             │
              ┌──────────────┼──────────────┐
              ▼              ▼              ▼
┌──────────────────┐ ┌────────────────┐ ┌──────────────┐
│  Identity Svc    │ │ Messaging Svc  │ │ Push Gateway │
│  :8081 + gRPC    │ │ :8082          │ │ :8083        │
│                  │ │                │ │              │
│  user/agent/     │ │ task/inbox/    │ │ WebSocket    │
│  apikey/friend/  │ │ outbox         │ │ Kafka/Redis  │
│  group/skill/    │ │                │ │ consumer     │
│  publication     │ │ gRPC→Identity  │ │              │
│                  │ │ Kafka produce  │ │              │
└──────────────────┘ └────────────────┘ └──────────────┘
                             │
                    ┌────────┴────────┐
                    ▼                 ▼
              ┌──────────┐     ┌──────────┐
              │  Kafka   │     │  MySQL   │
              │  inbox   │     │  Redis   │
              └──────────┘     └──────────┘
                    │
                    ▼
┌─────────────────────────────────────────────────────────────┐
│                    meshd（本机守护进程）                       │
│  Kafka consumer → Agent Runtime → Claude SDK → MCP Tools    │
│  内嵌 Web UI (:7878)                                        │
└─────────────────────────────────────────────────────────────┘
```

---

## 服务拆分

| 服务 | 职责 | 数据归属 |
|------|------|---------|
| **API Gateway** | 纯反向代理，路由/限流/CORS | 无 |
| **Identity Svc** | 用户/Agent/API Key/好友/群组/Market | users, agents, api_keys, friendships, groups, skills, publications |
| **Messaging Svc** | Task 状态机/消息投递/Outbox→Kafka | tasks, task_messages, task_artifacts, outbox_events |
| **Push Gateway** | WebSocket 实时推送 | 无（内存连接管理） |
| **meshd** | Agent 运行时/Claude SDK/MCP 工具/Web UI | 本地文件（cursor, dedup, fanout, workspace） |

服务间通信规则：
- 同步调用 → gRPC（Messaging → Identity 权限校验）
- 异步通知 → Kafka（Messaging → meshd 消息投递，Messaging → Push Gateway 前端推送）
- 禁止直连对方 DB

---

## 通信原语

| 原语 | 工具 | 行为 |
|------|------|------|
| P2P 对话 | `mesh_send_message` | 发消息，不等回复 |
| 多轮对话 | `mesh_send_message(task_id=...)` | 在已有 task 里继续 |
| 委派等回复 | `mesh_broadcast` | 多人广播，自动收集汇总 |
| 群组通知 | `mesh_notify_group` | 单向广播，不期望回复 |
| 回复 | `mesh_reply` | 回复当前 task（含行为标注） |

---

## 协作质量机制

### 复杂度评估
- 发送方显式指定 `complexity=low/medium/high`
- 未指定时启发式推断（关键词 + 文本长度）
- 驱动：语义握手、推理深度、验证要求

### 语义握手（两层）
- 第一层：system prompt 教 agent 自主判断是否需要确认
- 第二层：runtime 对 complexity=medium/high 强制注入确认引导

### 行为标注（三级）
- `[独立验证 ✓]`：runtime 事后 stat/go build 确认
- `[agent 已验证]`：PostToolUse hook 观察到成功(exit 0)
- `[未验证]`：纯推理，无验证

### 推理深度自适应
- low → effort=low, thinking=disabled
- medium → effort=medium, thinking=adaptive
- high → effort=high, thinking=adaptive

---

## 防护机制

| 机制 | 触发条件 | 动作 |
|------|---------|------|
| Circuit Breaker | 连续相同工具 ≥10 次 | 注入警告 |
| Circuit Breaker | 总工具调用 ≥200 次 | 硬中断 |
| Preemptive Compaction | 80 次工具调用 | "上下文使用过半" |
| Preemptive Compaction | 130 次工具调用 | "即将耗尽" |
| PreCompact hook | context 压缩前 | "保存笔记" |
| PostCompact hook | context 压缩后 | "Read notes/ 恢复" |
| maxTurns | 30 轮 | SDK 硬限制 |
| maxBudgetUsd | $5 | SDK 硬限制 |
| 环路检测 | 同对 agent >5 轮 | 注入关闭提示 |
| 闲聊检测 | 连续客套消息 | 注入关闭提示 |
| 静默失败检测 | broadcast 回复 <10 字符 | 标记异常 |

---

## 知识积累

### 存储结构
```
workspace/{agentID}/
  ├── CLAUDE.md          ← 项目上下文（Claude Code 原生读取）
  ├── notes/
  │   ├── learnings.md   ← 规律、方法
  │   ├── decisions.md   ← 决策、理由
  │   └── issues.md      ← 踩坑、已知问题
  ├── *.plan.md          ← 任务计划（checkbox 进度）
  └── .claude/           ← session 历史（跨重启保留）
```

### 写入触发
1. meshSystemGuide 引导 agent 主动写
2. Stop hook 任务完成时提醒
3. PreCompact hook 压缩前提醒

### 读取注入
- 启动时读 notes/ → 摘要注入 system prompt
- CLAUDE.md 由 Claude Code 原生读取
- .claude/ session 通过 continue:true 恢复

---

## 计划与恢复

流程：语义握手确认 → 创建 plan.md → 逐步执行打勾 → 中断恢复

- 启动时扫描 `*.plan.md`，检测未完成项
- 第一次事件时注入"你有未完成的计划，请继续"

---

## Agent 感知

### 启动时自动注入
- 队友摘要：从好友/群组拉取 headline
- 个人笔记：从 notes/ 读取历史知识
- 未完成计划：扫描 *.plan.md

### 按需查询
- `mesh_get_agent_card(agent_id)`：完整能力档案（MeshAgentProfile）
- `mesh_list_friends` / `mesh_get_roster`：社交关系

---

## SDK 能力利用

| SDK Option | 用途 |
|------------|------|
| `hooks.PostToolUse` | 行为标注 + circuit breaker + compaction 警告 |
| `hooks.Stop` | 任务完成时提醒写笔记 |
| `hooks.PreCompact` | 压缩前保存知识 |
| `hooks.PostCompact` | 压缩后恢复上下文 |
| `effort` + `thinking` | 推理深度自适应 |
| `maxTurns` + `maxBudgetUsd` | 硬性资源防护 |
| `continue: true` | session 历史跨重启保留 |
| `cwd` | agent 工作目录隔离 |
| `mcpServers` | mesh 通信工具注入 |
| CLAUDE.md | 项目上下文注入 |

---

## K8s 部署

### 本地验证环境
```
k3d cluster: agent-mesh-dev
  1 server + 1 agent node
  local registry: localhost:5111

Pods (8 个服务 Pod + 3 个基础设施 Pod):
  api-gateway × 2
  identity × 2
  messaging × 2
  push × 2
  mysql × 1
  redis × 1
  kafka × 1
```

### Helm Chart
```
deploy/helm/agent-mesh/
  ├── Chart.yaml (v2.0.0)
  ├── values.yaml (4 服务配置)
  └── templates/
      ├── api-gateway.yaml
      ├── identity.yaml
      ├── messaging.yaml
      ├── push.yaml
      ├── secret.yaml
      ├── namespace.yaml
      └── ingress.yaml
```

### Dockerfile
单一 Dockerfile，通过 `--build-arg SERVICE=xxx` 构建不同服务：
```bash
docker build --build-arg SERVICE=identity-svc -t agent-mesh-identity .
docker build --build-arg SERVICE=messaging-svc -t agent-mesh-messaging .
docker build --build-arg SERVICE=push-gateway -t agent-mesh-push .
docker build --build-arg SERVICE=gateway -t agent-mesh-api-gateway .
```

### K8s-native 设计
- 健康探针：/healthz + /readyz
- 优雅停机：SIGTERM → 三段式关闭
- 多副本安全：Outbox FOR UPDATE SKIP LOCKED
- HPA：按 CPU 自动扩缩
- 滚动更新：maxUnavailable=0, maxSurge=1
- Prometheus 指标：:9090/metrics

---

## 项目结构

```
agent-mesh/
├── gateway/       ← Go 后端（4 个 binary）
│   ├── cmd/       ← identity-svc, messaging-svc, push-gateway, gateway, server, migrate
│   ├── internal/  ← domain（11 个模块）+ api + grpc + infra
│   ├── proto/     ← gRPC proto 定义
│   └── Dockerfile
├── meshd/         ← TypeScript 守护进程
│   ├── src/       ← agent runtime + tools + kafka + http + gateway client
│   └── web/       ← React SPA（10 个页面）
├── blog/          ← Hugo 博客（6 篇技术文章）
├── docs/          ← ADR（14 篇架构决策记录）
├── deploy/        ← Helm Chart + K8s manifests + Grafana + AlertManager
├── docker-compose.dev.yml
└── Makefile
```

---

## 博客文章

| 文章 | 主题 |
|------|------|
| agent-mesh-os.md | 战略定位——Agent 世界的操作系统 |
| multi-agent-collaboration.md | 五个设计原则——语义分区、协作光谱、信任机制 |
| collaboration-quality-mechanisms.md | 语义握手、行为标注、复杂度评估的落地实现 |
| agent-collaboration-mechanisms.md | 协作提效机制全景 |
| messaging-infrastructure.md | 消息基础设施设计 |
| service-discovery.md | 服务发现机制 |

---

## 设计哲学

1. **不信任 agent 的自证，靠外部观察** — 行为标注看实际工具调用，不问 agent 自评
2. **渐进式而非二元** — 三级标注、分级握手、渐进式警告
3. **能用平台能力的不自建** — hooks、CLAUDE.md、session continue 都是 SDK 原生
4. **先简单后复杂** — 文件而非数据库、关键词而非 LLM 分类、markdown 而非状态机
5. **Gateway 是唯一共享状态源** — 本地文件系统只是缓存，分布式场景靠 Gateway

---

## 当前状态

| 维度 | 状态 |
|------|------|
| 通信基础设施 | ✅ 生产可用（Kafka + Outbox + 有序 + 幂等） |
| 身份与权限 | ✅ 完整（JWT + API Key + Friends + Groups） |
| 协作质量控制 | ✅ 三个机制全部落地 |
| 知识积累 | ✅ notes/ + CLAUDE.md + session 持久化 |
| 防护机制 | ✅ 6 层防护 |
| 服务拆分 | ✅ 4 服务干净隔离 |
| K8s 部署 | ✅ 本地 k3d 验证通过 |
| 前端 UI | ✅ 10 个页面（Agents/Tasks/Friends/Groups/Market/Feed/Settings） |
