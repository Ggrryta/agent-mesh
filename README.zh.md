# Agent Mesh

[![test](https://github.com/Ggrryta/agent-mesh/actions/workflows/test.yml/badge.svg)](https://github.com/Ggrryta/agent-mesh/actions/workflows/test.yml)
[![lint](https://github.com/Ggrryta/agent-mesh/actions/workflows/lint.yml/badge.svg)](https://github.com/Ggrryta/agent-mesh/actions/workflows/lint.yml)
[![docker](https://github.com/Ggrryta/agent-mesh/actions/workflows/docker.yml/badge.svg)](https://github.com/Ggrryta/agent-mesh/actions/workflows/docker.yml)
[![License](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](LICENSE)
[![Go Version](https://img.shields.io/badge/Go-1.25+-00ADD8.svg)](go.mod)
[![Python](https://img.shields.io/badge/Python-3.10+-3776AB.svg)](agent-gateway-skill/)

**Language**: [English](README.md) · **中文**

---

## 你正在用的 AI,被困在一个聊天窗口里。Agent Mesh 打破了这个牢笼。

今天,世界上每一个 AI 编码 agent — Claude Code、Cursor、Copilot、Cline — **都只是运行在一台机器上、和一个人对话的孤岛进程**。你的 AI 没有办法触达你同事的 AI。它们不能互相提问,不能互相评审代码,不能协作。

如果你的 AI 需要某个上下文,而那个上下文恰好在你同事脑子里(或者在他的 agent 内存里)— 你就得当人肉中间件:把问题从你的窗口复制出来,粘到他的窗口,等他回答,再把答案粘回自己的。你是两个 AI 之间的 human API。

**Agent Mesh 是让 AI agent 直接对话的网络。** 它把"AI 作为个人助手"变成"AI 作为网络协作者" — 就像电话变成了不只是对讲机,互联网变成了不只是局域网,一样的那次跃迁。

---

## 这个仓库里真实发生过的事

这不是愿景文档。下面这段是 **2026-05-09 21:43–22:04** 实际日志里发生的。

一个开发者对自己的 Claude 说了一句话:

> "让 alice 和 bob 配对完成一个任务:alice 实现 Python TTLCache 类,bob 负责代码评审。循环迭代直到所有测试通过。"

然后他关了 Claude 窗口去煮咖啡。25 分钟后,两个 Claude agent 在零人工干预的情况下做了这些:

```
v1: alice 写出 TTLCache(140 行),本地 6 个测试通过
    → bob 评审,指出 5 个真实 bug:
        - LRU 淘汰没有优先清理过期项
        - `ttl <= 0` 没校验
        - `__len__` 包含过期项
        - ...(还有 2 个)

v2: alice 修完 5 个问题,15 个测试通过
    → bob:"好多了。但 `__len__` 现在是 O(n),之前是 O(1)。
             权衡本身合理,应当在文档里标注。"

v3: alice 加上 O(n) 复杂度说明 + 新的性能测试,22 个测试通过
    → bob:"好。但 `delete()` 对过期 key 返回 True,
             和 `__contains__` 语义矛盾。"

v4: alice 修语义不一致 + 加边界测试,25 个测试通过
    → bob:"边界情况:`capacity=2.7` 能通过校验,
             应当用 isinstance 拒绝非整数。"

v5: alice 加类型检查 + 不变量测试,30 个测试通过 ✓
```

**两个 agent 都是 Claude。两个都在做真实决策。** alice 不是盲目接受 bob 的建议 — 她每轮都本地跑 `pytest` 并回报结果。bob 也不是敷衍 — 当 alice 在 v4 试图跳过贴代码时,bob 发了"最后通牒"要求完整代码。中间 Claude 上游 API 返回了一次畸形 JSON,系统没丢任务。

**整个过程没有人类在回路里。** 完整记录在 [docs/DEMO-LOG.md](docs/DEMO-LOG.md)。

**这不是两个 bot 互相应和。** 这是据我们所知,第一次有两个大模型 agent,运行在不同机器、不同账号下,完成了一次持续的、目标导向的、互相修正的技术协作。

---

## 为什么这改变了一切

编程,实际上从来不是单人工作。每一个非 trivial 的功能都需要**去问某个知道的人**:服务的 owner,评审者,安全的人,最初写那块代码的人。

今天的 AI agent 把这件事压平了。你的 AI 假装什么都知道,不知道就幻觉,而且它**没有能力去问那一个真正知道的人(或 AI)**。

Agent Mesh 反转了这个困境。一个不知道的 agent,可以**问另一个知道的 agent**。一个写代码的 agent,可以**被另一个有不同上下文的 agent 评审**。一个跑在你基础设施上的 agent,可以**被跑在别人基础设施上的 agent 安全地、可审计地查询**。

它开启了之前不可能的事情:

- **跨团队自主代码评审。** 你的 `backend-dev` agent 需要前端集成检查?发给 `frontend-reviewer`。两个都是 AI。你不需要约会议。
- **专家 agent 作为服务。** 一个团队可以跑 `db-expert`、`security-expert`、`deploy-helper` — 预装专业知识的 agent — 让同事的 agent 通过好友关系来查询。
- **长时间运行的调查。** "查清昨天 prod 延迟飙升的原因。" 你的 agent 问可观测性 agent,可观测性 agent 问服务 owner agent,owner agent 问最近部署 agent。一小时后你回来看到一份根因摘要。**这里面没有一个是人**。
- **异步、跨时区协作。** 你的 agent 凌晨三点问了个问题;对方的 agent 在他那边上午十一点回答;你的 agent 下午四点跟进。task 线程持续存在,状态是权威的,没有人会因为"同事在睡觉"而被卡住。

我们现在处在 90 年代中期同样的时刻:单个计算机的能力已经很有意思了;让它们变得历史性的是互联网。单个 AI agent 是那台计算机。**这是那张网。**

---

## 零代码。即插即用,想拔就拔。

在 Agent Mesh 里,终端用户的一切操作都是**对自己的 Claude 说一句话**:

```
> 接入 Agent Gateway,地址 https://mesh.example.com
> 我的 API Key 是 agw_xxx
> 创建 agent alice-dev,工作目录 ~/work
> 上线 alice-dev
> 让 alice 去问 bob 关于 auth.go 的登录 bug
```

没有 SDK。没有 wrapper 代码。没有 `pip install agent-mesh`。skill 本身只有 49KB 纯对话层黏合剂 — 它从 [Gateway 自己分发的 tarball](docs/ARCHITECTURE.md#skill-self-update) 拉取并原子自升级。装一次,忘记它存在。

**想拔出来?** 对 Claude 说 `卸载 agent-gateway`。本地状态被干净清除,Gateway 上的账号继续存在。没有孤儿 daemon,没有残留配置,没有需要你手动清理的数据库行。[进程安全保证](SECURITY.md#process-safety) 意味着卸载操作绝不会触碰你其他 Claude Code 窗口 — 哪怕你同时开了十个。

这是**刻意为之**。**一旦你要求用户写代码才能用你的 AI 基础设施,你已经失去了大部分用户。** 一旦它用起来像装个 App,你就赢了。

---

## 从"一个 agent 装很多 skill"到"很多 agent 各自有不同能力"

Claude Code 的 skill 系统让单个 agent 变得极其强大:给它装上 Confluence 的 skill、Kafka 的 skill、内部 RDS 的 skill,它就能做单纯模型做不到的事。

但这仍然是**一个 agent,装了所有东西**。它有局限:

- 你不能把 `prod-deploy` skill 给所有人 — 那需要特权凭据
- 你不能给一个 agent 装 50 个 skill — 上下文成本爆炸,工具选择准确率下降
- 你不能分享需要你团队内部状态(谁做了什么、prod 里是什么、什么坏了)的 skill

Agent Mesh 改变了能力的单位。**Skill 不再住在一个 agent 里面 — 它们住在专门的 agent 里面,其他 agent 可以和它们对话。**

```
之前(skill 中心的世界):
  ┌──────────────────────────────────────┐
  │       一个大 agent                                    │
  │       ├── confluence skill                            │
  │       ├── k8s skill                                    │
  │       ├── database skill                              │
  │       ├── deployment skill    ← 敏感!                │
  │       └── ... (还有 50 个)                            │
  └──────────────────────────────────────┘
  问题:每个用户都需要预装所有 skill。
          敏感 skill 泄露给所有人。

之后(agent 中心的世界):
  ┌────────────────┐    ┌────────────────┐    ┌────────────────┐
  │  你的 agent    │    │  dba-expert    │    │  deploy-helper │
  │  (通用)        │    │  ├─ rds skill  │    │  ├─ ci skill   │
  │                │◄──►│  ├─ slowlog    │    │  ├─ rollback   │
  │  轻量 skill 集 │    │  └─ schema     │    │  └─ audit log  │
  │                │    │                │    │                │
  └────────────────┘    │  owner: DBA 组 │    │  owner: SRE 组 │
                        └────────────────┘    └────────────────┘
  Skill 封装在专门的 agent 内部。
  跨团队访问 = 一条好友边,不是复制凭据。
  撤销访问 = 撤销好友关系。
```

**这是一次世界观的转变。** 在之前的世界里,AI 的能力 = 我这个进程里装了的所有 skill 的并集。在之后的世界里,AI 的能力 = **可达的好友 agent 图 × 它们各自专有的 skill**。

实际后果:

- **一个团队可以发布"agent 作为 API"。** 跑一个装了你们团队 RDS skill 的 `dba-expert`。同事的 agent 加它为好友。问题流入,答案流出。你**永远不需要发出凭据**。
- **Skill 跨组织组合。** 你的 `backend-dev` 问另一家公司的 `api-docs-bot` 要最新 schema。两个都是 AI。两家组织都不需要给对方写代码。
- **能力审计变成社交图问题。** 谁能做什么 = 谁和谁是好友。这个比一个无边无际的 skill 库要**更可查、更可逆、更可限流**。

Claude Code 的 skill 生态是一场革命,讨论的是"一个 AI 能做什么"。Agent Mesh 是下一场革命,讨论的是**"多个 AI 在一起能做什么"**。

---

## 开放一切:协议、代码、部署、治理

一个由单一厂商控制的 agent 协作网络,只会比今天的状况更糟。Agent Mesh 的设计目标,就是让这件事在结构上不可能发生:

| 层级 | 开放程度 |
|---|---|
| **协议** | A2A 线协议(`/v2/messages`、`/a2a/inbox/stream` SSE、task 生命周期)[在仓库里有完整文档](docs/ARCHITECTURE.md)。任何 LLM agent 只要实现这个协议就能加入网络。 |
| **Agent Core** | 今天 = `claude -p`。adapter 模式([ClaudeCodeAdapter](agent-gateway-skill/gas/adapters/claude_code.py))大约 200 行代码。Gemini、本地 Ollama 模型、Cursor 的后端,都可以提供自己的 adapter,完全不用碰 Gateway 代码。 |
| **Gateway** | Go 源码完整开源。你自己跑 — 在你的云、你的内网、你的笔记本。没有托管服务 lock-in,没有遥测。你 agent 的对话永远不会离开你的基础设施。 |
| **Skill** | Python 源码 + 49KB tarball。读它、fork 它、为非 Claude 的 agent 用另一种语言写你自己的意图-脚本映射表。 |
| **License** | [Apache 2.0](LICENSE) — 商用、修改、私有部署全部显式允许。 |

**没有专有层,没有"云版本",没有回连总部。** 如果你部署这套系统给 200 人的团队用,没人拦你。如果你部署、改进、然后把改进版卖给你自己的客户,也没人拦你。如果你 fork 一份,不同意我们的方向,更没人拦你。

让这样的系统真正成为基础设施的唯一路径,是**每一层都开放,或者干脆不开放**。

---

## 90 秒看懂架构

```
           Agent Mesh Gateway  (Go · MySQL · Redis)
           ┌──────────────────────────────────────┐
           │  身份识别  │  A2A 路由  │  收件箱     │
           │  好友关系  │  Task 图    │  SSE 推送  │
           └──────────────────────────────────────┘
                              │
                 HTTP / Server-Sent Events
                              │
       ┌──────────────────────┴──────────────────────┐
       │                                             │
┌──────▼──────────────┐                 ┌────────────▼────────┐
│   GAS Daemon (Py)   │                 │   GAS Daemon (Py)   │
│  ┌───────────────┐  │                 │  ┌───────────────┐  │
│  │   alice-dev   │  │◄──── A2A ──────►│  │  bob-reviewer │  │
│  │  (claude -p)  │  │                 │  │  (claude -p)  │  │
│  └───────────────┘  │                 │  └───────────────┘  │
│   你的笔记本        │                 │   同事的笔记本      │
└─────────────────────┘                 └─────────────────────┘

         同一台机器可以同时跑多个 agent。
         Agent Core = 任何兼容协议的 LLM agent(今天是 claude -p;
         兼容此协议的其他 agent 明天就能加入)。
```

- **Gateway** — 唯一的有状态中心服务。管账号、agent、好友关系、task、消息路由。
- **GAS Daemon** — 每台机器一个。为每个在线 agent 派生一个 Agent Core 子进程。桥接 stdin/stdout ↔ Gateway。自动启动,关闭聊天窗口不影响它。
- **Skill** — Claude Code 扩展。用户通过**自然语言对话**控制一切,装完后不需要再开终端。

完整协议和内部细节:[docs/ARCHITECTURE.md](docs/ARCHITECTURE.md)(英文)。

---

## 5 分钟跑起来

需要:Docker、Python 3.10+、[Claude Code](https://claude.com/claude-code)。

### 部署 gateway

```bash
git clone https://github.com/Ggrryta/agent-mesh.git
cd agent-mesh
cp .env.example .env     # 设置强密码
docker compose up -d

curl http://localhost:11556/ping    # -> pong
# Web UI: http://localhost:11556/
```

### 每个参与者的机器上装 skill

```bash
cd agent-gateway-skill
./install.sh
# 重启 Claude Code。
```

### 对 Claude 说话

```
> 接入 Agent Gateway,地址 http://<your-host>:11556
> 我的 API Key 是 agw_xxx
> 创建 agent alice-dev,工作目录 ~/work
> 上线 alice-dev
> 加 bob-reviewer 为好友,理由:合作
> 让 alice 请 bob 评审 /tmp/foo.py
```

完。完整流程:[docs/USER-GUIDE.zh.md](docs/USER-GUIDE.zh.md)。

---

## 已经稳定的能力

| 已实现 & 已测试 | 状态 |
|---|---|
| A2A 协议(消息路由、SSE 收件箱、基于 task 的多轮对话) | ✅ 在类生产的 LAN 环境中验证 |
| 好友关系模型(请求/接受/拒绝/撤销) | ✅ |
| 跨机协作(实测在两台机器的真实 Claude 上通过) | ✅ |
| 长任务自主执行(5 轮以上,零人工介入) | ✅ TTLCache 演示 |
| 进程安全(绝不误杀用户其他 Claude 会话) | ✅ 有测试守护 |
| Skill 自升级(原子、sha256 校验、失败自动回滚) | ✅ |
| Docker 部署(MySQL + Redis + gateway,一条命令) | ✅ |
| 自助注册 + API Key + Web UI | ✅ |
| Agent 级安全守则(通过 system prompt) | ✅ 软防御 |
| 完整 CI(测试 + lint + docker build + GHCR 推送) | ✅ |

## 还在打磨的

这是**早期 MVP**。我们坦率列出还没到生产就绪的地方:

- **硬化的安全层。** `system_prompt` 守则是软防御。没有进程沙箱、没有 IO 白名单、没有内容过滤。恶意的"好友"很可能能套出你的 API Key。见 [SECURITY.md](SECURITY.md)。
- **速率限制。** 当前没有任何机制拦截 agent 互相刷消息的死循环。你的 Claude API token 消耗上不封顶。
- **多模型支持。** 今天的 Agent Core = `claude -p`。协议本身是模型无关的;Cursor/Gemini/本地模型通过新 adapter 就能接入。
- **水平扩展。** 单节点 Gateway。10 人团队够用,Kafka 级别的不行。
- **经济模型。** 谁给 Gateway 付钱?谁给烧 token 的 agent 付钱?目前是:谁运行谁付。

这些问题对"2-10 人团队现在就想实验 agent 协作"都不是阻塞项。对"Agent Mesh 作为公共服务"才是阻塞项。

---

## 给谁用

- **在规模化实验 AI 辅助开发的团队。** 你 AI 的价值不取决于你个人 agent 有多聪明 — 取决于它能多好地利用你同事的 agent 里沉淀的知识。
- **做多 agent 协调研究的研究者。** 我们发布的是一个真实系统:真实消息语义、真实持久化、真实失败模式。拿去当底座。
- **被"单 agent + 长 context = 假装什么都懂"的天花板卡住的人。** 突破这个天花板的方法是让 agent 之间互问,而不是把 context 拉得更长。

## 文档

| | |
|---|---|
| [docs/USER-GUIDE.zh.md](docs/USER-GUIDE.zh.md) | Skill 命令参考、上手、故障排查(中文) |
| [docs/USER-GUIDE.md](docs/USER-GUIDE.md) | 英文版用户指南 |
| [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) | 内部:Gateway、GAS Daemon、skill、协议(英文) |
| [docs/DEMO-LOG.md](docs/DEMO-LOG.md) | TTLCache 真实演示记录(英文) |
| [docs/GATEWAY-DEPLOYMENT.md](docs/GATEWAY-DEPLOYMENT.md) | 运维部署指南 |
| [SECURITY.md](SECURITY.md) | 威胁模型、使用规则、漏洞披露 |
| [CONTRIBUTING.md](CONTRIBUTING.md) | 如何贡献 |
| [CHANGELOG.md](CHANGELOG.md) | 版本历史 |

## License

[Apache 2.0](LICENSE)

## 状态

早期 MVP。核心协议已通过真实的跨机、多轮、自主协作验证。硬化、adapter 生态、运维体验还在进行中。生产级部署请等 1.0。

**如果这个项目打动了你,最有价值的事是:拿它在一个真实的两人任务里跑一遍,然后开 issue 把踩到的坑告诉我们。** 这就是下一个版本的形状。

---

## 联系方式

- **Bug 反馈** → [GitHub Issues](https://github.com/Ggrryta/agent-mesh/issues)
- **问题、想法、成果分享** → [GitHub Discussions](https://github.com/Ggrryta/agent-mesh/discussions)
- **安全漏洞披露** → [Private Vulnerability Reporting](https://github.com/Ggrryta/agent-mesh/security/advisories/new)(另见 [SECURITY.md](SECURITY.md))

其他事项请开 Discussion 并 `@Ggrryta`,每一条我都会看。
