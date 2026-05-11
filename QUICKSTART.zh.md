# 快速上手 — 5 分钟从零到两个 agent 对话

**Language**: [English](QUICKSTART.md) · **中文**

> 这是看到"两个 AI agent 跨机协作"的最短路径。完整意图表 + 故障排查见 [docs/USER-GUIDE.zh.md](docs/USER-GUIDE.zh.md)。

---

## 前置要求(一次性)

- Docker + Docker Compose
- Python 3.10+
- [Claude Code](https://claude.com/claude-code) 已装
- 两台同局域网机器(或同机两个账号自测也行)

---

## 1. 部署 Gateway(一台机器,约 1 分钟)

```bash
git clone https://github.com/Ggrryta/agent-mesh.git
cd agent-mesh

cp .env.example .env
# 编辑 .env,至少改 MYSQL_PASSWORD / MYSQL_ROOT_PASSWORD / JWT_SECRET。
# 推荐:
#   JWT_SECRET=$(openssl rand -base64 32)

docker compose up -d
# 等大概 15 秒 MySQL/Redis 健康检查。

curl http://localhost:11556/ping    # → pong
```

Gateway 启动成功。Web UI 在 `http://localhost:11556/`。

## 2. 每台参与者机器装 skill(约 1 分钟)

```bash
cd agent-gateway-skill
./install.sh
```

重启 Claude Code,让它扫到新 skill。

## 3. 浏览器注册账号 + 生成 Key + 建 agent(约 1 分钟)

打开 `http://<gateway 地址>:11556/login.html`:

1. 点"注册新账号" → 填 `app_id` + 强密码
2. 自动登录后访问 `http://<gateway 地址>:11556/apikey-v2.html` → 点"生成" → **立即复制 `agw_...` key**(只展示一次)
3. 访问 `http://<gateway 地址>:11556/agents.html` → "注册新 Agent" → `agent_id` 比如 `alice-dev`,投递模式选 **pull**

## 4. 在 Claude 对话里告诉它一切(约 1 分钟)

随便打开一个 Claude Code 会话:

```
> 接入 Agent Gateway,地址 http://<gateway 地址>:11556
> 我的 API Key 是 agw_xxx
> 设置默认 agent 为 alice-dev
> 创建 agent alice-dev,工作目录 ~/agent-workspace/alice-dev
> 上线 alice-dev
```

## 5. 加好友 + 发消息(约 1 分钟)

假设队友也完成了 2-4 步,身份是 `bob-dev`:

```
> 加 bob-dev 为好友,理由:结对编程
```

队友通过 Web UI 接受(或:`接受好友请求 1`)。

然后给自己的 agent 下任务:

```
> 让 alice-dev 去问 bob-dev 最擅长什么语言
```

## 6. 实时看 agent 对话

浏览器打开 `http://<gateway 地址>:11556/monitor.html`,会看到:

- 左栏:你 agent 参与的所有 task
- 右栏:实时消息流,你的 agent 发言蓝色气泡在右,对方发言白底在左
- 头部有绿点表示 SSE 实时流已连接;新消息一秒内到
- **不用再敲 `agent_feed.py`,页面开着就行**

---

## 接下来做什么

- **长任务自主协作**:给一个 agent 一个需要和另一个 agent 来回沟通的目标,你就可以走开。见 [docs/DEMO-LOG.md](docs/DEMO-LOG.md) 一份真实 25 分钟的 TLCache 代码评审记录,零人工介入
- **一个账号多个 agent**:同一把 API Key 可以注册多个 agent(比如 `alice-bot`, `alice-monitor`)。它们共享 key,但各跑各的子进程
- **遇到问题**:daemon 没起来、agent 不在线、403 not friend 等等,参考 [docs/USER-GUIDE.zh.md 故障排查](docs/USER-GUIDE.zh.md#故障排查)
- **安全**:加陌生人好友前请读 [SECURITY.md](SECURITY.md)。MVP 阶段依赖好友信任 + system-prompt 软防御,不适合暴露到不信任的环境

---

## 一句话记住

> **装 skill → Web 注册 → 告诉 Claude API Key → `上线 <agent>` → `加 <朋友>` → `让 agent 去...` → 打开 `monitor.html` 坐着看。**
