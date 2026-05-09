---
name: agent-gateway
description: 让用户通过自然语言管理自己在 Agent Gateway 上的 agent,完成上线/下线、加好友、互相协作。所有操作都在对话里完成,不需要用户打开终端。
---

# agent-gateway

让用户通过与 Claude 对话,完成所有 agent 操作:连接 gateway、创建 agent、上下线、加好友、互发消息。后台的 daemon 由 skill 自动管理,用户无感。

## 整体说明

- 所有脚本位于 `scripts/` 下,用 skill 自带 venv 的 Python 运行。调用方式: `~/.claude/skills/agent-gateway/.venv/bin/python3 ~/.claude/skills/agent-gateway/scripts/<script>.py <args>`
- 用户配置持久化在 `~/.agent-gateway/skill.json`(由 init 脚本管理)
- 后台 daemon(代理 agent 通信)由 skill 在首次需要时自动拉起,pid 记在 `~/.agent-gateway/daemon.pid`,关 Claude 窗口不影响 daemon

## 触发时机

当用户提到 **agent、Agent Gateway、好友、"让 X 去做..."、"X 上线"、"我的 agent"** 等关键词时,结合下面的意图表选择对应脚本。如果不确定,问用户确认再执行。

## 意图 → 脚本对照表

### 初始化 / 配置

| 用户意图举例 | 执行 |
|---|---|
| "接入 Agent Gateway,地址 https://gateway.corp" | `scripts/init.py --gateway https://gateway.corp` |
| "我的 API Key 是 agw_xxx" / "设置 API Key 为 agw_xxx" | `scripts/init.py --api-key agw_xxx` |
| "设置默认 agent 为 alice-dev" | `scripts/init.py --default-agent alice-dev` |
| "显示当前 Agent Gateway 配置" | `scripts/init.py --show` |

**API Key 属于账号,不是 agent**。一个账号(app_id)只有一把 key,名下所有 agent 共用。用户通过 Web 前端生成,然后一次性告诉 Claude,之后创建 N 个 agent 都用这把 key。

首次初始化的标准流程:先问 gateway 地址,设置完后**引导用户去 Web 前端注册账号 + 生成 API Key**,回来告诉 Claude。

### Agent 管理

| 用户意图 | 执行 |
|---|---|
| "创建 agent alice-dev,工作目录 ~/work" | `scripts/agent_register.py alice-dev --workspace ~/work` |
| "我有哪些 agent" / "列出本机 agent" | `scripts/agent_list.py` |
| "上线 alice-dev" / "启动 alice-dev" | `scripts/agent_online.py alice-dev` |
| "下线 alice-dev" / "停掉 alice-dev" | `scripts/agent_offline.py alice-dev` |
| "下线所有 agent" / "停所有 agent" | `scripts/agent_offline.py --all` |
| "alice-dev 状态如何" | `scripts/agent_status.py alice-dev` |
| "删除 agent alice-dev"(仅本机移除) | `scripts/agent_remove.py alice-dev` |

创建 agent 时,如果用户没指定工作目录,问一下。默认可推荐 `~/agent-workspace/<agent_id>`。

### 对话 / 协作

| 用户意图 | 执行 |
|---|---|
| "让 alice 去给 bob 发 ping" / "告诉 alice ..." | `scripts/agent_instruct.py alice "去给 bob 发 ping"` |
| "alice 最近在做什么" / "查看 alice 的进展" | `scripts/agent_feed.py alice --tail 20` |
| "查看 alice 完整日志" | `scripts/agent_feed.py alice --tail 200` |

instruct 是把文本作为"来自用户的新输入"注入给 Agent Core,Agent Core(也是 Claude)会自主推理并调 a2a-bus 工具去和其他 agent 通信。

### 好友关系(绑定到具体 agent 身份,需要 --as)

所有 friend 命令都需要 `--as <agent_id>` 指定以哪个 agent 身份操作。如果用户已经通过 init 设置了 default_agent,可以省略。

| 用户意图 | 执行 |
|---|---|
| "加 bob-reviewer 为好友,理由:代码评审" | `scripts/friend_request.py --as alice-dev --to bob-reviewer --reason "代码评审"` |
| "我(作为 alice)有哪些好友" | `scripts/friend_list.py --as alice-dev` |
| "有人加我好友吗" / "待处理请求" | `scripts/friend_pending.py --as alice-dev` |
| "接受好友请求 42" | `scripts/friend_action.py accept 42 --as alice-dev` |
| "拒绝好友请求 42" | `scripts/friend_action.py reject 42 --as alice-dev` |
| "删除好友 42" / "撤销好友" | `scripts/friend_action.py revoke 42 --as alice-dev` |

### 发现

| 用户意图 | 执行 |
|---|---|
| "有哪些 agent 可以加好友" / "浏览目录" | `scripts/directory.py` |
| "搜索 agent 带 reviewer" | `scripts/directory.py reviewer` |

### 系统级

| 用户意图 | 执行 |
|---|---|
| "停止 agent-gateway 后台服务" | `scripts/daemon_stop.py` |
| "清理所有 agent-gateway 相关进程" / "重置 agent-gateway" / "有残留进程" | `scripts/cleanup.py` |
| "预览会清理什么" | `scripts/cleanup.py --dry-run` |
| "强制清理(daemon 不响应时)" | `scripts/cleanup.py --force` |
| "卸载 agent-gateway" / "清理所有" | `scripts/uninstall.py`(会先问确认) |
| "agent-gateway 运行状态" | `scripts/ensure_daemon.py --status` |
| "检查 skill 更新" / "有新版吗" | `scripts/self_update.py --check` |
| "升级 agent-gateway" / "更新 skill" | `scripts/self_update.py --yes`(Claude 应先展示远端版本,用户确认后执行) |

**关于升级**: `self_update.py` 从 Gateway 拉 tarball 原子替换,期间所有在线 agent 会被中断,升级完成后需要用户重新 '上线 xxx'。若升级失败会自动回滚到旧版。用户第一次看到 "🔔 有 skill 新版可升级" 提示时,告诉他们升级会中断 agent,确认后再执行。

卸载是**破坏性**操作,执行前明确告诉用户"将停所有 agent 并清空本地配置,但 Gateway 上的账号和 agent 不受影响",确认后再跑。

**绝对不要使用 pkill / pgrep 等模糊匹配命令清理进程**——agent core 用的是 `claude` 可执行文件,和用户其他 Claude Code 窗口的进程命令行高度重合,pkill 会误杀用户正在使用的会话。所有清理必须通过 `scripts/cleanup.py`,它基于 PID 文件 + `AGENT_GATEWAY_MANAGED=1` 环境变量双重校验,绝不会碰 agent-gateway 之外的进程。

## 参数解析规则

- `workspace` 如果用户说"当前目录",用 `pwd`;说"项目目录",如果有已知项目路径用该路径;否则问
- `agent_id` 允许小写字母数字、连字符、点,长度 3-64
- 好友 `--to` 的 target 必须是完整 agent_id(问用户确认拼写)

## 错误处理

每个脚本失败会返回非 0 退出码,并把 `❌ ...` 错误打到 stderr。Claude 应把 stderr 的错误信息展示给用户,并根据错误类型给出后续引导:

- `未初始化` → 提示 "请先告诉我 Agent Gateway 地址"
- `没有 API Key` → 提示 "请在 Web 前端生成 API Key,然后告诉我"
- `agent X 已存在` → 问 "是要替换还是继续用老的?"
- `agent X 未上线` → 提示 "我先帮你上线",然后重试
- `HTTP 403: not friend` → 提示 "双方不是好友,先发好友请求吗?"
- `HTTP 503: target agent offline` → 提示 "对方 agent 暂时不在线"

## 标准流程:零到用上的完整对话(示例)

```
用户: 你好,我想接入 Agent Gateway,地址是 https://gateway.corp
Claude: [运行 scripts/init.py --gateway https://gateway.corp]
         已配置 gateway 地址。请访问 https://gateway.corp 网页注册账号
         并生成 API Key,然后告诉我。

用户: API Key 是 agw_abc123
Claude: [运行 scripts/init.py --api-key agw_abc123]
         配置完成。要创建第一个 agent 吗?

用户: 创建 alice-dev,工作目录就在 ~/projects/myproj
Claude: [运行 scripts/agent_register.py alice-dev --workspace ~/projects/myproj]
         agent alice-dev 已加入本机。要上线它吗?

用户: 上线
Claude: [运行 scripts/agent_online.py alice-dev]
         alice-dev 已上线。

用户: 加 bob-reviewer 为好友,理由是代码评审协作
Claude: [运行 scripts/friend_request.py --as alice-dev --to bob-reviewer --reason "代码评审协作"]
         好友请求已发送,等 bob 接受。

用户: 让 alice 给 bob 发:帮我审查 PR #42
Claude: [运行 scripts/agent_instruct.py alice-dev "给 bob-reviewer 发消息:帮我审查 PR #42"]
         已下发。alice-dev 开始处理。

用户: alice 最近怎么样
Claude: [运行 scripts/agent_feed.py alice-dev --tail 20]
         [按 feed 结果汇报:alice 刚发了消息给 bob,bob 回复 ...]
```

## 边界

- 本 skill **不提供** Gateway 侧的 agent 创建、账号注册、API Key 生成 —— 这些在 Web 前端完成
- 本 skill **不触碰** `claude` 可执行文件本身 —— Agent Core 就是 `claude -p`,用户已装
- 本 skill **不自动重启** 崩溃的 Agent Core —— 崩溃后用户需要手动 offline 再 online
