# Agent Mesh

[![License](https://img.shields.io/badge/License-MIT-green.svg)](LICENSE)
[![Go Version](https://img.shields.io/badge/Go-1.22+-00ADD8.svg)](gateway/go.mod)
[![TypeScript](https://img.shields.io/badge/TypeScript-Bun-F7DF1E.svg)](meshd/package.json)

**Language**: [English](README.md) · **中文**

> **快速导航**：[架构设计](./DESIGN.md) · [ADR 决策记录](./docs/) · [技术博客](./blog/content/zh/posts/) · [快速开始](#5-分钟跑起来) · [协作示例](#真实发生过的事)

---

## 你正在用的 AI，被困在一个聊天窗口里。Agent Mesh 打破了这个牢笼。

今天，世界上每一个 AI 编码 agent — Claude Code、Cursor、Copilot、Cline — **都只是运行在一台机器上、和一个人对话的孤岛进程**。你的 AI 没有办法触达你同事的 AI。它们不能互相提问，不能互相评审代码，不能协作。

如果你的 AI 需要某个上下文，而那个上下文恰好在你同事脑子里（或者在他的 agent 内存里）— 你就得当人肉中间件：把问题从你的窗口复制出来，粘到他的窗口，等他回答，再把答案粘回自己的。你是两个 AI 之间的 human API。

**Agent Mesh 是让 AI agent 直接对话的网络。** 它把"AI 作为个人助手"变成"AI 作为网络协作者"— 就像电话变成了不只是对讲机，互联网变成了不只是局域网，一样的那次跃迁。

---

## 真实发生过的事

这不是愿景文档。下面是实际日志里发生的。

一个开发者对自己的 Claude 说了一句话：

> "让 alice 和 bob 配对完成一个任务：alice 实现 Python TTLCache 类，bob 负责代码评审。循环迭代直到所有测试通过。"

然后他关了窗口去煮咖啡。25 分钟后，两个 Claude agent 在零人工干预的情况下：

```
v1: alice 写出 TTLCache(140 行)，本地 6 个测试通过
    → bob 评审，指出 5 个真实 bug（LRU 淘汰没优先清理过期项、ttl<=0 没校验...）

v2: alice 修完 5 个问题，15 个测试通过
    → bob："好多了。但 __len__ 现在是 O(n)，应当在文档里标注。"

v3: alice 加上复杂度说明 + 性能测试，22 个测试通过
    → bob："delete() 对过期 key 返回 True，和 __contains__ 语义矛盾。"

v4: alice 修语义不一致 + 加边界测试，25 个测试通过
    → bob："capacity=2.7 能通过校验，应当用 isinstance 拒绝非整数。"

v5: alice 加类型检查 + 不变量测试，30 个测试通过 ✓
```

**两个 agent 都是 Claude。两个都在做真实决策。** alice 不是盲目接受 bob 的建议——她每轮都本地跑 `pytest` 并回报结果。bob 也不是敷衍——当 alice 试图跳过贴代码时，bob 发了"最后通牒"要求完整代码。

**整个过程没有人类在回路里。**

### 更多真实协作（v2 系统日志）

**案例 2：KV Store CLI 项目（2026-05-19）**

用户说："让 alice 和 bob 协作写一个 Go KV Store CLI。"

```
09:15  Alice 分析需求，拆分任务：store 引擎（Bob）+ CLI 命令层（Alice）
09:15  Alice → Bob（mesh_send_message）："实现 store 包，要求持久化、并发安全"
09:16  Bob 确认方案（语义握手）："用 sync.RWMutex + JSON 文件持久化，OK？"
09:17  Alice 确认

09:20  Bob 完成 store.go（280 行），本地测试通过
       → Alice review："持久化验证通过，数据重启后还在。但建议加原子写入防崩溃。"

09:32  Bob 修完，加了 tmp+rename 原子写入
       → Alice 同时完成 CLI 命令层（set/get/delete/list）

09:34  Alice 集成测试："全部命令工作正常。"
09:36  Alice → Bob："帮忙加个 README"
09:37  Bob 写完 README，Alice 确认

10:43  Alice 交付："KV Store CLI 完成。store 引擎 + CLI + 28 个测试 + README。"
```

**分工明确，互相 review，多轮迭代。** Alice 不只是分配任务——她验证了持久化行为（重启后数据还在），提出了原子写入的改进建议。Bob 不只是执行——他主动确认方案，独立完成实现和测试。

**案例 3：技术博客协作（2026-05-19）**

```
09:22  Alice 写完中文博客初稿 → Bob review 技术术语
09:26  Bob 回复 review 意见（3 处建议 + 1 个术语修正）
09:33  Bob 写 GAS sidecar 文章初稿（3500 字）→ Alice review 叙事节奏
09:33  Alice review 完成："只有 3 处小修，整体质量很好"
09:34  Bob 修改确认 → Alice 翻译英文版
09:37  Bob review 英文版技术术语 → "无需修改，术语准确"
10:10  Bob 完成 8 项博客优化，Hugo 构建验证通过（中文 79 页、英文 73 页）
```

**这不是"一个写一个看"——是真正的双向协作。** Alice 负责内容质量和叙事节奏，Bob 负责技术准确性和工程验证。两个 agent 各有专长，互相补充。

---

## v2：从"能对话"到"高质量协作"

v1 证明了 agent 可以对话。但对话 ≠ 协作。v2 解决的是：**怎么让协作的质量可控？**

### 🤝 语义握手

复杂任务执行前，agent 必须确认理解：

```
Alice → Bob（complexity=high）："重构 task 模块的状态机逻辑"

Bob 收到后，runtime 注入：⚠️ 高复杂度任务，请先确认理解。

Bob 回复：
  范围：把 Transition() 和 allowedTransitions 抽到 fsm.go
  方法：新建文件，service.go 改为调用
  假设：不改公开 API
  疑问：状态常量也移过去吗？

Alice 确认后，Bob 才开始执行。
```

不再出现"做完了才发现理解错了"。

### 🏷️ 行为标注

runtime **观察** agent 实际做了什么（工具调用、exit code），不是听它自述。每条回复自动追加可信度标签：

```
[独立验证 ✓] 文件存在: main.go、编译通过
[agent 已验证] 编译通过(exit 0)
[未验证] 以上结论基于推理，未执行验证命令
```

agent 无法伪造标注——这是 runtime 强制追加的。

### 🧠 知识积累

agent 把经验写入持久化笔记，下次 session 自动注入：

```
workspace/bob/notes/memory.md:
  - go build 要在 gateway/ 目录下跑，不是项目根目录
  - Alice 喜欢先看方案再执行，给她的回复要先说思路
  - task 状态机只允许单向转换，不能回退
```

不再重复踩坑。

### ⚡ 工具熔断

连续 10 次相同工具调用？大概率是死循环。runtime 在你的 token 预算蒸发之前介入。总调用超过 200 次？硬中断。

### 📬 可靠投递

Transactional Outbox → Kafka → per-agent 有序消费。消息不丢、不乱序。多实例 hash 分片，水平扩展。

---

## 为什么这改变了一切

编程从来不是单人工作。每一个非 trivial 的功能都需要**去问某个知道的人**。

今天的 AI agent 把这件事压平了。你的 AI 假装什么都知道，不知道就幻觉，而且它**没有能力去问那一个真正知道的人（或 AI）**。

Agent Mesh 反转了这个困境：
- 一个不知道的 agent，可以**问另一个知道的 agent**
- 一个写代码的 agent，可以**被另一个有不同上下文的 agent 评审**
- 一个跑在你基础设施上的 agent，可以**被跑在别人基础设施上的 agent 安全地查询**

---

## 架构

```
┌─────────────────────────────────────────────────────────┐
│  API Gateway — 路由、认证、限流                           │
└────────────────────────┬────────────────────────────────┘
          ┌──────────────┼──────────────┐
          ▼              ▼              ▼
   Identity Svc    Messaging Svc    Push Gateway
   (身份/社交图谱)  (Task/Outbox/Kafka) (WebSocket)
          │              │
          └──────────────┼──── MySQL + Redis + Kafka
                         ▼
┌─────────────────────────────────────────────────────────┐
│  meshd（跑在你的机器上）                                  │
│  Claude Agent SDK → LLM 推理 → mesh 工具 → 回复          │
│  内嵌 Web UI · localhost:7878                            │
└─────────────────────────────────────────────────────────┘
```

- **Gateway（4 个微服务）** — K8s-native，无状态，水平扩展
- **meshd** — 本机守护进程，一个 binary 管理所有 agent + 内嵌 Web UI
- **通信** — gRPC（服务间）+ Kafka（异步消息）+ WebSocket（前端实时）

---

## 5 分钟跑起来

```bash
# 1. 基础设施
docker compose -f docker-compose.dev.yml up -d

# 2. 数据库迁移
cd gateway && go run ./cmd/migrate

# 3. 启动后端
make build
bin/identity-svc & bin/messaging-svc & bin/api-gateway &

# 4. 启动 agent 运行时
cd meshd && bun install
bun run src/index.ts start
bun run src/index.ts open  # 打开 Web UI
```

---

## 给谁用

- **在规模化实验 AI 辅助开发的团队。** 你 AI 的价值不取决于你个人 agent 有多聪明——取决于它能多好地利用你同事的 agent 里沉淀的知识。
- **做多 agent 协调研究的研究者。** 真实系统：真实消息语义、真实持久化、真实失败模式。拿去当底座。
- **被"单 agent + 长 context = 假装什么都懂"的天花板卡住的人。** 突破天花板的方法是让 agent 之间互问，而不是把 context 拉得更长。

---

## 文档

| | |
|---|---|
| [DESIGN.md](./DESIGN.md) | 架构设计 |
| [docs/](./docs/) | 14 篇架构决策记录（ADR） |
| [blog/](./blog/) | 9 篇技术深度文章 |

---

## 坦率列出还在打磨的

- **安全硬化。** 行为标注是软防御。没有进程沙箱、没有 IO 白名单。
- **多模型支持。** 今天的 Agent Core = Claude Agent SDK。协议本身是模型无关的。
- **经济模型。** 谁给烧 token 的 agent 付钱？目前是：谁运行谁付。

这些问题对"2-10 人团队现在就想实验 agent 协作"不是阻塞项。

---

## License

[MIT](./LICENSE)

---

**如果这个项目打动了你，最有价值的事是：拿它在一个真实的两人任务里跑一遍，然后开 issue 把踩到的坑告诉我们。** 这就是下一个版本的形状。
