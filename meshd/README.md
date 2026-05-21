# agent-meshd

> Agent-Mesh local daemon. One process manages many autonomous agent workers.

Built on **Claude Agent SDK**. meshd 监听 `127.0.0.1:7878`，提供本机控制 API
和（M2 起）内嵌 Web UI。一个二进制把所有东西打包好——用户装好之后用
`agent-meshd start` / `stop` / `open` 简单管理，不需要懂 systemd / launchd。

## 当前阶段（M1.3 完成）

- ✅ 多 agent worker 管理（Map<agentID, AgentRuntime>）
- ✅ HTTP server: `/api/health`, `/api/instances`, `/api/auth/*`
- ✅ Keychain 凭证存储（macOS Keychain / 加密文件 fallback）
- ✅ Localhost token 鉴权
- ✅ Device-flow 登录
- ✅ 单二进制（Bun --compile）
- ✅ 简单进程管理：`start` / `stop` / `restart` / `status` / `open` / `logs`
- ⏳ M2：内嵌 Web UI

## 安装

### 方式 A：本地编译（开发）

```bash
cd meshd
bun install
bun build src/index.ts --compile --outfile dist/agent-meshd
./dist/agent-meshd help
```

### 方式 B：install.sh（一行命令）

```bash
# 用本地二进制
INSTALL_DIR=$HOME/.local/bin BINARY_PATH=./dist/agent-meshd ./install.sh

# 从 GitHub Release 下载（待发布）
curl -fsSL https://example.com/install.sh | sh
```

## 使用

```bash
# 设环境变量（一次性）
export GATEWAY_URL=http://localhost:8080
export ANTHROPIC_AUTH_TOKEN=sk-...
# 公司代理可选：
# export ANTHROPIC_BASE_URL=https://your-proxy/api

# 启动
agent-meshd start
# → agent-meshd started (pid 12345, port 7878).

# 看状态
agent-meshd status

# 浏览器打开 UI（M2 之后真有 UI；当前只是 /api/* JSON）
agent-meshd open

# 看日志
agent-meshd logs -f

# 停止
agent-meshd stop
```

`start` 是后台启动（fork + detach），命令立即返回。`run` 是前台跑（service
manager / docker / 调试用）。

## 配置

| 环境变量 | 必填 | 默认 | 说明 |
|---------|------|------|------|
| `GATEWAY_URL` | ✅ | - | Gateway 地址，如 `http://localhost:8080` |
| `ANTHROPIC_AUTH_TOKEN` / `ANTHROPIC_API_KEY` | ✅ | - | Anthropic 凭证（worker 共享） |
| `ANTHROPIC_BASE_URL` | ❌ | 官方 API | 公司代理可在此覆盖 |
| `MESHD_HOST` | ❌ | `127.0.0.1` | 仅 loopback，不要改 |
| `MESHD_PORT` | ❌ | `7878` | 端口 |
| `STATE_DIR` | ❌ | `~/.agent-mesh` | state.json / cursor / 日志 |
| `MODEL` | ❌ | `claude-sonnet-4-5` | 默认模型 |
| `POLL_WAIT_SEC` | ❌ | `20` | inbox long-poll 等待时长 |
| `LOG_LEVEL` | ❌ | `info` | `debug` / `info` / `warn` / `error` |

## Architecture

```
agent-meshd (单进程)
  ├─ HTTP server :7878 (loopback)
  │   ├─ /api/health
  │   ├─ /api/auth/me, /device/start, /device/cancel, /logout
  │   ├─ /api/instances           # 列出本机在跑的 worker
  │   └─ POST /api/instances/:id  # start / stop
  ├─ AgentManager
  │   └─ Map<agentID, AgentRuntime>
  │       ├─ AuthManager (API Key → JWT, auto-refresh)
  │       ├─ Heartbeat (30s)
  │       ├─ Inbox poller (long-poll, cursor 持久化)
  │       └─ Claude Agent SDK query loop
  ├─ UserAuth (device-flow → user JWT in keychain)
  ├─ SecretStore (macOS Keychain / 加密文件 fallback)
  │   ├─ user_jwt
  │   └─ api_key:<agentID>
  ├─ StateStore (~/.agent-mesh/state.json)
  └─ RuntimeInfoStore (~/.agent-mesh/runtime.json)
       ├─ pid                 # 给 stop / status 用
       ├─ port                # 实际监听端口
       └─ auth_token          # 本机 API 鉴权 cookie token
```

## 设计参考

- ADR 013：GAS 重写为 TypeScript + Claude Agent SDK
- ADR 014：meshd 本机服务 + 内嵌 UI（当前里程碑）
