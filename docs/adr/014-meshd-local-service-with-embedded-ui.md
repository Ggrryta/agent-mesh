# ADR 014: meshd 本机服务 + 内嵌 Web UI

**Status**: Accepted
**Date**: 2026-05-14

## Context

Week 6 完成了 Gateway 后端 + 独立 SPA 前端的基本面板。Week 7 期间用真实 agent 端到端验证后，暴露了"用户接入门槛过高"的问题：

1. **启动 agent 实例靠手动 `bun run`**：用户要 clone gas-ts、装 Bun、`export` 环境变量，整套不像消费级软件
2. **API Key 没法在前端签发**：后端 API 已存在，但前端没 UI；用户要手动 `curl` 才能拿到 raw_key 配给 gas-ts
3. **没有"统一管理面"**：身份在前端建、API Key 用 curl 拿、进程在终端跑、心跳在前端看——四个地方割裂
4. **跨机器同身份冲突**：用户在两台机器分别启动 alice 时，inbox cursor 互相覆盖

ADR 013 把 GAS 重写成基于 Claude Agent SDK 的 TypeScript 进程，已经脱离了 Claude Code 桌面客户端。但 gas-ts 仍是"一进程一 agent"的开发态形态，离"装 app 一样简单"差一段距离。

## Decision

把 **gas-ts 升级为本机常驻服务 `agent-meshd`**，并将 Gateway 的 Web 前端**内嵌进 meshd 二进制**，由 meshd 在 `127.0.0.1:7878` 上托管。Gateway 后端继续是中心，所有持久化数据（agents / tasks / timelines / market / friendships / groups）都在 Gateway。

```
本机                                       云端
┌──────────────────────────────┐           ┌─────────────┐
│  agent-meshd (单二进制)       │ ←─HTTPS─→ │  Gateway    │
│   ├─ HTTP server :7878 (loopback)        │  - users    │
│   │   ├─ /         ← 内嵌 SPA            │  - agents   │
│   │   ├─ /api/*    ← 本机控制 API        │  - tasks    │
│   │   └─ /ws/*     ← 实时事件            │  - groups   │
│   ├─ Agent workers (Map<agentID, runtime>)              │
│   ├─ Anthropic 客户端 (Claude Agent SDK)                 │
│   └─ 持久化:                                            │
│       ~/.agent-mesh/state.json   运行实例清单           │
│       ~/.agent-mesh/cursor/*     inbox cursor          │
│       系统 keychain              API Key + JWT          │
└──────────────────────────────┘           └─────────────┘
       ↓ 浏览器访问                                ↓
   http://localhost:7878                     api.anthropic.com
```

用户视角的全流程：

```bash
# 1. 一行命令安装
curl -fsSL https://mesh.example/install.sh | sh
# 装好二进制（不注册任何系统服务，不开机自启）

# 2. 配置 + 启动
export GATEWAY_URL=http://localhost:8080
export ANTHROPIC_AUTH_TOKEN=sk-...
agent-meshd start            # 后台跑
agent-meshd open             # 浏览器打开 UI
# 第一次访问：device-flow 登录
# 之后：进入控制台

# 3. 控制台一站式管理
# - 创建 agent 身份 + 写 system_prompt
# - "在本机运行" 开关 → meshd 自动签 API Key + 起 worker
# - Market 一键 fork 别人的 agent
# - 群组 / 任务 / 时间线全在同一面板
```

故意不做"开机自启"——桌面应用场景下用户常嫌弃自启服务后台占资源。
用户每次开机自己 `agent-meshd start`（或 `agent-meshd open` 自动 start）即可。
真要自启，自己挂 launchd plist / systemd unit / cron @reboot。

## Alternatives Considered

### 1. 保持 gas-ts + 给前端补 API Key 抽屉
让用户在前端生成 raw_key、自己手动配 env、自己 `bun run`。**否决**：解决了"API Key 没法签"，但"启动进程靠 CLI" + "管理面割裂"两个核心问题没解。

### 2. 把 gas-ts 容器化，用户跑 docker run
**否决**：要求用户装 Docker，且容器内文件系统隔离让"系统 keychain 存凭证 / state 持久化 / 守护进程"变难。不符合"装 app 一样简单"。

### 3. meshd 不内嵌前端，前端继续由 Gateway 托管
**否决**：浏览器从 Gateway 域名访问 UI，UI 要远程调 meshd 的 localhost API → 跨域。即便配 CORS，安全模型变复杂（要保护 localhost API 不被任意网站脚本调用）。同源同进程是最干净的。

### 4. 在 Claude Code / Cursor 等宿主里以 MCP 形式接入
ADR 013 已经否决——宿主是被动模型，无法触发自主协作。

### 5. 多机自动主备切换
**推迟**：V1 假设"同一 agent 同时只在一台 meshd 上跑"，第二台启动时 Gateway 报 `agent already running`。多机抢锁 / 故障转移留给 V2，不阻塞当前里程碑。

## Consequences

### 正面

- **零门槛接入**：用户不开终端、不装依赖、不写 env，浏览器一站式
- **凭证安全升级**：raw_key 永远不进浏览器，由 meshd 直接写系统 keychain
- **统一管理面**：身份 / 实例 / 市场 / 群组 / 任务全在 meshd UI，一个 URL 搞定
- **离线友好**：UI 和 daemon 同进程，本地操作瞬时响应；Gateway 抖动时 UI 不卡
- **架构没大改**：Gateway 后端继续是中心，agent 协作模型不变；现有前端代码 ~95% 可以原样迁入 meshd

### 负面

- **平台分发成本**：要维护 macOS / Linux / Windows 三个二进制 + 安装脚本 + 自动升级
- **二进制体积**：Bun `--compile` + 内嵌前端 dist + Anthropic SDK 估计 ~80MB
- **跨机器协作受限**：V1 同一 agent 不能多机同时跑，用户多设备场景要手动切换。多机协调留给 V2

### 中性

- **Gateway 后端零改动**：所有 API 已经具备，meshd 只是新的前端 + 新的执行层
- **Gateway 是否还托管前端**：建议**取消**，Gateway 变成纯 API 服务器；ops 视角的管理后台后续再补，目前不需要

## Implementation

分四个里程碑（详见 PLAN.md §5 Week 9）：

| 里程碑 | 目标 |
|------|------|
| **M1: 本机服务化** | gas-ts → meshd（HTTP server + 多 worker + state.json + start/stop 进程管理 CLI） |
| **M2: UI 迁入 + 凭证打通** | frontend 迁入 meshd/web，device-flow 登录，"在本机运行"开关 |
| **M3: Settings + 管理面板补齐** | Settings 页 + 现有页面适配 meshd API |
| **M4: Market** | `agent_publications` 表 + API + Market UI |

完成 M1+M2+M3 即满足"装 app 一样简单 + 统一管理面"两个核心需求；M4 是市场，可滞后。

## 关键技术决策

### 打包

- **Bun `--compile`** 单二进制，三平台（macOS arm64 / linux x64 / windows x64）
- 前端 Vite build 产物用 Bun 的资源嵌入机制打进二进制
- install.sh 检测平台 → 下载对应二进制到 `/usr/local/bin` 或 `~/.local/bin` → 输出 next-step 提示（设环境变量 + `agent-meshd start`）

### 端口

- 固定 `127.0.0.1:7878`，被占用 +1 重试
- 真实端口写到 `~/.agent-mesh/runtime.json`，CLI `agent-mesh open` 读这个文件拼 URL

### 鉴权

- **浏览器 ↔ meshd**：核心安全边界是 **loopback only 监听 + 文件 mode 0600 的 runtime.json**——同机当前用户已经能信任。在此前提下加一层"随机 token cookie"作为防御性栏杆：
  - meshd 启动生成 token，写 `~/.agent-mesh/runtime.json`（mode 0600）
  - SPA 资源路径（非 `/api/*`）**无条件 set-cookie 到当前 token**——这样浏览器刷新或 meshd 重启后访问 / 都能自动拿到当前有效 cookie，不会陷入 401 死循环
  - `/api/*` 严格校验 cookie 匹配；不匹配时**主动 set-cookie 清掉旧值** + 401
  - `agent-meshd open` 命令仍兼容 `?t=token` 入口（首次开浏览器免依赖 SPA 路径）
- **meshd ↔ Gateway**：device-flow 拿 user JWT，存系统 keychain（macOS Keychain / Linux libsecret / Windows DPAPI）。失效自动 refresh
- **meshd ↔ Gateway**：device-flow 拿 user JWT，存系统 keychain（macOS Keychain / Linux libsecret / Windows DPAPI）。失效自动 refresh
- **agent worker ↔ Gateway**：每个 worker 自己持有 API Key（也存 keychain），按 ADR 009 的 token refresh 机制换短期 JWT

### 多机互斥

Gateway 加一张轻量表 `agent_runtime_locks`（agent_id PK + meshd_id + last_heartbeat），meshd 启动 worker 时尝试 INSERT，失败说明别处在跑 → 报错给 UI。心跳过期 60s 自动释放。这个表跟 agent 心跳分开（agent 心跳是 active 状态语义；这个锁是"哪个物理 meshd 在跑"语义）。

### 状态恢复

`~/.agent-mesh/state.json`:

```json
{
  "version": 1,
  "instances": [
    { "agent_id": "alice", "auto_start": true, "last_started_at": "..." }
  ]
}
```

meshd 启动时读这个文件，对每个 `auto_start: true` 的 agent 自动起 worker。

## References

- ADR 013：GAS 重写为 TypeScript + Claude Agent SDK（铺垫）
- ADR 007：API Key + JWT（凭证模型）
- ADR 009：Client token refresh（worker 续签）
- 类似产品参考：Plex / Jellyfin / Docker Desktop / Home Assistant Core

