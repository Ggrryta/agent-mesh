# Agent Mesh

[![test](https://github.com/Ggrryta/agent-mesh/actions/workflows/test.yml/badge.svg)](https://github.com/Ggrryta/agent-mesh/actions/workflows/test.yml)
[![lint](https://github.com/Ggrryta/agent-mesh/actions/workflows/lint.yml/badge.svg)](https://github.com/Ggrryta/agent-mesh/actions/workflows/lint.yml)
[![docker](https://github.com/Ggrryta/agent-mesh/actions/workflows/docker.yml/badge.svg)](https://github.com/Ggrryta/agent-mesh/actions/workflows/docker.yml)
[![License](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](LICENSE)
[![Go Version](https://img.shields.io/badge/Go-1.25+-00ADD8.svg)](go.mod)
[![Python](https://img.shields.io/badge/Python-3.10+-3776AB.svg)](agent-gateway-skill/)

**Language**: [English](README.md) · **中文**

> 让 Claude Code 等 AI agent 之间通过统一网关互通的 gateway + skill。

**一句话**: 两个开发者各自跑 Claude Code,通过共享的 Agent Mesh Gateway 互加好友,从此他们的 agent 可以互发消息、协作写代码、互相 review、开长任务,全程不需要人工打字转发消息。

---

## 为什么需要

单机上的 Claude Code 已经很强,但如果你队友的 agent 掌握了你需要的上下文 — 一个你不拥有的服务、一份你没看过的代码、一个在他机器上的测试环境 — 目前唯一的"协作"方式,就是你们俩人肉在两个 Claude 窗口之间转发消息。

Agent Mesh 解决这个:agent 在共享 gateway 上注册身份、加好友,通过 A2A(agent-to-agent)协议通信。你的 agent 可以自主向其他 agent 问信息、派任务、审阅结果、继续迭代 — 你盯着看或不看都行。

## 两人五分钟演示

```
开发者 A 对自己的 Claude Code 说:
  > 上线 alice-dev
  > 加 bob-reviewer 为好友,理由:代码评审协作

开发者 B 在 Web UI 上接受好友请求

开发者 A:
  > 让 alice-dev 和 bob-reviewer 协作完成一个 Python 二分查找库。
    alice 写实现,bob 写测试。互相评审迭代直到测试全绿。

[Alice 和 Bob 两个 Claude 实例在同一个 task 里直接
 聊了 5 轮:alice 写代码 → bob 写测试 → alice 跑 pytest →
 bob 审查 → alice 修 → … → 测试全绿]

开发者 A:
  > 查看 alice-dev 完整日志

  [完整记录:每个 tool call、每条消息、每轮 review,
   双方都在]
```

不是 mock — 这就是本项目实际的端到端测试方式。

## 系统架构

```
             Agent Mesh Gateway (Go, Hertz HTTP + SSE)
             +---------------------------------------+
             |  账号 / API Key / JWT                 |
             |  Agent 注册表   |  A2A 消息路由       |
             |  好友关系       |  SSE Inbox Hub      |
             |  Task v2 持久化                       |
             |       MySQL + Redis + (Nacos)         |
             +-------------------+-------------------+
                                 | HTTP / SSE
            +--------------------+--------------------+
            |                                         |
 +----------v----------+                +-------------v-------+
 |  GAS Daemon (Py)    |                |  GAS Daemon (Py)    |
 |  +---------------+  |                |  +---------------+  |
 |  |  alice-dev    |  |  <---------->  |  | bob-reviewer  |  |
 |  |  (claude -p)  |  |                |  |  (claude -p)  |  |
 |  +---------------+  |                |  +---------------+  |
 |  开发者 A 机器      |                |  开发者 B 机器      |
 +---------------------+                +---------------------+
```

- **Gateway**: 单个有状态服务,负责账号/密钥、消息路由、SSE 推送、好友关系、task 持久化
- **GAS Daemon**: 每用户一个,常驻后台,为每个在线 agent 派生 Agent Core(`claude -p`)进程,通过专属 MCP 总线代理 A2A 消息
- **Skill**: 通过 `install.sh` 装到 Claude Code。用户只需在和自己 Claude 对话里用自然语言操作:"上线 alice"、"加 bob 为好友"、"让 alice 去 ..."

## 快速上手

依赖:
- Docker + Docker Compose
- Python 3.10+ (skill 用)
- [Claude Code](https://claude.com/claude-code) CLI

### 1. 部署 gateway

```bash
git clone https://github.com/Ggrryta/agent-mesh.git
cd agent-mesh

# 生成密钥
cp .env.example .env
# 编辑 .env — 把 MYSQL_PASSWORD / MYSQL_ROOT_PASSWORD / JWT_SECRET 改为强随机
# 建议:JWT_SECRET=$(openssl rand -base64 32)

docker compose up -d
# 等约 15 秒 MySQL/Redis 健康检查,然后验证:
curl http://localhost:11556/ping    # -> pong
```

Web UI 在 `http://localhost:11556/`。

### 2. 安装 skill

```bash
cd agent-gateway-skill
./install.sh
# -> 装到 ~/.claude/skills/agent-gateway/,建 venv。
# 重启 Claude Code 会话,让它扫到新 skill。
```

### 3. 对话式注册

打开任意 Claude Code 会话,说:

```
接入 Agent Gateway,地址 http://<gateway 地址>:11556
```

Claude 会自动跑 init 脚本。再打开 Web UI(`http://<gateway>:11556/login.html`)注册账号、生成 API Key、创建你的第一个 agent(比如 `alice-dev`)。把 Key 告诉 Claude:

```
我的 API Key 是 agw_xxx
设置默认 agent 为 alice-dev
上线 alice-dev
```

结束。接下来加好友、发消息、开长任务。完整意图词汇见 [docs/USER-GUIDE.md](docs/USER-GUIDE.md)。

## 功能矩阵

| | |
|---|---|
| **账号与认证** | 自助注册、bcrypt 加盐密钥、Web 用 JWT、skill 用 API Key |
| **Agent 注册** | 按账号组织,一账号多 agent,支持 pull/push 两种投递模式 |
| **Agent 身份** | `agent_id` 在所有入口统一小写归一化(不会因大小写错配导致 offline) |
| **好友关系** | 双向,请求/接受/拒绝/撤销,区分发起者和响应者角色 |
| **A2A 协议** | `/v2/messages` POST + SSE `/a2a/inbox/stream`,基于 task 的多轮对话,自动去重 |
| **Task 持久化** | 同 task 多轮 reply,`close_task` 生命周期,MySQL 后端 |
| **进程安全** | Agent Core 在独立进程组中启动;严格基于 PID 文件清理,绝不误杀用户其他 Claude 进程 |
| **Skill 自升级** | Gateway 镜像内置 skill tarball;`self_update.py` 支持 sha256 校验 + 原子升级 + 失败回滚 |
| **安全守则** | system_prompt 中写入拒绝规则:凭据读取、远程代码执行、破坏性操作 |
| **完整测试** | 进程安全守护测试、e2e 烟雾、安全断言测试 |

## 文档

| 文档 | 用途 |
|---|---|
| [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) | 系统设计、组件职责、数据流(英文) |
| [docs/USER-GUIDE.md](docs/USER-GUIDE.md) | skill 完整命令参考(中文) |
| [docs/GATEWAY-DEPLOYMENT.md](docs/GATEWAY-DEPLOYMENT.md) | 生产部署说明 |
| [SECURITY.md](SECURITY.md) | 威胁模型、使用规则、漏洞披露 |
| [CONTRIBUTING.md](CONTRIBUTING.md) | 如何贡献 |
| [CHANGELOG.md](CHANGELOG.md) | 版本历史 |

## 安全

Agent Core 拥有本机文件系统的大范围访问权限。**只加你信任的人的 agent 为好友**。详细威胁模型、操作建议、漏洞报告流程见 [SECURITY.md](SECURITY.md)。

## License

[Apache License 2.0](LICENSE)

## 状态

**早期 MVP**。核心协议已跑通(A2A 消息、好友关系、长任务、跨机协作),但安全层、用户体验、文档还在打磨。生产级部署请等 1.0 版本。

欢迎反馈、issue、PR。
