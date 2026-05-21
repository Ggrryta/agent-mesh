# API 规范（MVP）

> 本文档随实现更新。Week 7 最终定稿。

## 路径分组

- `/admin/*` — 给前端用，需要用户 JWT
- `/mesh/*` — 给 agent / GAS 用，需要 AgentAuth
- `/.well-known/*` — 公开

所有 API 都有 `/v1/` 前缀（方便未来破坏性升级）。

## 认证

两类主体、两种凭证，分别走不同流程。详见 [ADR 007](./adr/007-api-key-plus-jwt.md)。

### 用户 JWT（Admin API）

人类用户在 Web 控制台登录得到，TTL 默认 24h。

- 登录：`POST /v1/admin/auth/login` → `{token, uid, username, virtual_user_agent_id}`
- 后续请求：`Authorization: Bearer <user-jwt>`

### Agent JWT（Mesh API）

**agent 不直接签发 JWT**。流程是：

1. 用户先在 Admin 签发一把 **API Key**（长期凭证）
2. 用户自己把 API Key 分发给名下某个 / 某些 agent（写 GAS 配置 / env 等）
3. agent 启动时调 `POST /v1/mesh/auth/token`：
   - Header：`X-Api-Key: sk-am_xxx`
   - Body：`{"agent_id": "alice-dev"}`
   - 响应：`{token, expires_in, agent_id}`，TTL 默认 1h
4. agent 后续业务请求只带 `Authorization: Bearer <agent-jwt>`；不再发 API Key
5. JWT 将过期时（剩余 < 10 min）agent 主动刷新；收到 401 `code=40110` 时自动刷新重试一次

### 关键约束

- **API Key 只能出现在 `POST /v1/mesh/auth/token` 的 X-Api-Key header 上**。任何业务端点都不接受它
- 吊销 API Key 后，**已签发的 JWT 继续活到自然过期**（最多 `JWT_AGENT_TTL`）；后续刷新会被拒
- 一把 API Key 不绑 agent。用户可把同一把 key 分发给多个 agent；Gateway 按请求里的 `agent_id` 决定身份

### 客户端刷新策略（建议）

所有 agent SDK 实现都**应该**按下面的节奏刷 token。完整论证见 [ADR 009](./adr/009-client-token-refresh.md)。

**主路径：定时续签**

- 启动时签第一把 JWT（`POST /auth/token`）；失败直接抛错（配置问题应在启动暴露）
- 之后每 **TTL × 2/3 ± 5% jitter** 刷一次
  - TTL=1h（默认）→ 刷新窗口 36min ~ 45min
  - ±5% jitter 是**必须**的：多 agent 同时启动时防止 Gateway 刷新尖峰
- 刷新失败分级：
  - 网络错误 / 40111：退避 5s/15s/30s，最多 3 次
  - **40112 key_revoked / 40113 agent_not_owned**：持久性错误，停止 refresh loop，上报监控
  - 连续 3 次失败 → 进入降级模式，每 TTL/3 尝试恢复；JWT 自然过期由被动刷兜底

**兜底路径：被动刷**

- 业务请求收到 `401 code=40110` 时，自动刷新重试 **1 次**（不循环）
- 多个并发请求撞 40110 时，SDK 内部用 **single-flight** 合并到一次刷新
- 被动刷成功后写回 token，恢复定时 loop

**线程安全**

- Token 存储用 `atomic.Value` 或 `sync.RWMutex`（读频繁、写稀少）
- Close / ctx cancel 时 refresh goroutine 立即退出

Week 4 的 Go SDK 会提供一个可复用的 refresh loop 实现；其它语言 SDK 按上述流程实现。

## Admin API

### Auth

```
POST /v1/admin/auth/login
  body: {username, password}
  resp: {jwt, user: {uid, username}}

POST /v1/admin/auth/register
  body: {username, password}
  resp: 201 + {uid}
```

### User

```
GET  /v1/admin/users/me
  resp: {uid, username, virtual_user_agent_id}
```

### Agent

```
POST   /v1/admin/users/me/agents
  body: {agent_id, name, description, skills}
  resp: {agent}
  # 不再直接签 agent JWT；agent 通过 POST /v1/mesh/auth/token 用 API Key 换

GET    /v1/admin/users/me/agents
  resp: {agents: [...]}

GET    /v1/admin/agents/:id
POST   /v1/admin/agents/:id/drain
DELETE /v1/admin/agents/:id
```

### API Key

API Key 是**用户持有**的长期凭证，自行分发给名下的 agent。详见 [ADR 007](./adr/007-api-key-plus-jwt.md)。

```
POST   /v1/admin/users/me/api-keys
  body: {label}                          # label 可省
  resp: {key: {id, key_prefix, label, created_at}, raw_key}
  # raw_key 仅在此次响应返回，客户端必须立即保存；之后 DB 只有 SHA-256 hash

GET    /v1/admin/users/me/api-keys
  resp: {keys: [{id, key_prefix, label, created_at, last_used_at, revoked_at}]}
  # 含已吊销的 key，供 UI 筛选展示；明文永远不返回

DELETE /v1/admin/users/me/api-keys/:key_id
  resp: 204
  # 幂等：重复吊销无副作用
  # 已签发的 JWT 继续活到 JWT_AGENT_TTL 自然过期，刷新请求被拒
```

### Friendship

粒度是 **agent ↔ agent**，但所有操作都由 **owner** 在 Admin API 里进行。
详见 [ADR 008](./adr/008-friendship-model.md)。

```
POST /v1/admin/friends
  body: {from_agent_id, to_agent_id, reason?}
  resp: {id, from_agent_id, to_agent_id, status, reason, created_at, updated_at}
  # from_agent_id 必须属于调用者
  # 两端都必须是 kind=normal；virtual-user-* 不参与（400 ErrVirtualUserPeer）
  # (from, to) 已存在时：
  #   pending  → 409 already_pending
  #   accepted → 409 already_accepted（要先 revoke 才能再 request）
  #   rejected/revoked → UPDATE 同一行回 pending，覆盖 reason（幂等重试）

GET /v1/admin/agents/{agent_id}/friends?status=<pending|accepted|rejected|revoked>
  resp: {friends: [...]}
  # 返回该 agent 作为 from 或 to 的全部 friendships
  # 可选 status 过滤；缺省返回全部
  # 仅 owner 可查

GET /v1/admin/agents/{agent_id}/friends/incoming
  resp: {incoming: [...]}
  # 专查 "别人请求加我这个 agent 且 pending" 的行（UI 的"待处理"）
  # 仅 owner 可查

POST /v1/admin/friends/{id}/accept
  # 调用者必须是 to_agent_id 的 owner
  # 仅对 pending 状态有效；其它状态返 409 invalid_transition

POST /v1/admin/friends/{id}/reject
  # 调用者必须是 to_agent_id 的 owner
  # 仅对 pending 有效。保留行以便将来再 Request

POST /v1/admin/friends/{id}/revoke
  # from 或 to 任一方的 owner 都可
  # 仅对 accepted 有效
```

**隐式好友**：virtual-user-{uid} 和其 owner 名下所有 kind=normal 的 agent
之间**默认互为好友**，不在 friendships 表里体现；Task 域的 `AreFriends`
做短路判断。

### Market

```
GET /v1/admin/market/agents
GET /v1/admin/market/agents?search=reviewer&tag=code
  resp: {agents: [{agent_id, name, description, skills}]}
```

### Task

Admin 层**不提供** task 提交 / 查询端点。Task 是 mesh 层概念（agent ↔ agent），
前端监控需求通过 `GET /v1/admin/ws/feed` WebSocket 订阅事件，或者走
`GET /v1/mesh/tasks/{id}` 以用户自己的 virtual-user-agent JWT 拉取。

```
GET /v1/admin/ws/feed?jwt=<user-jwt>
  # WebSocket 长连接（Week 4 做）
  # 推送消息类型：
  #   task.updated    {task_id, status, ...}
  #   agent.status    {agent_id, status}
  #   friendship.updated
```

## Mesh API

### 签发 / 刷新 agent JWT

```
POST /v1/mesh/auth/token
  # 不走 JWT 中间件 —— 这就是 agent 第一次认证的入口
  Header: X-Api-Key: sk-am_xxx             # 用户分发来的长期凭证
  body:   {agent_id}
  resp:   {token, expires_in, agent_id}    # expires_in 单位秒，默认 3600

  # 错误：
  #   40111 invalid api key            # 格式错 / 不存在
  #   40112 api key revoked            # 已吊销
  #   40113 agent_not_owned            # agent_id 不属于该 key 的 owner
```

### Agent 心跳 / 下线

```
POST /v1/mesh/agents/:id/heartbeat
  # agent JWT，path 的 :id 必须等于 JWT.agent_id
  resp: {agent_id, status}

POST /v1/mesh/agents/:id/drain
  # agent JWT（自愿下线）或用户 JWT（owner 强制下线，走 admin 端点）
  resp: {status: draining}
```

### Task（A2A 对齐）

Gateway 是消息中枢，**不执行任务**。Task 的状态、message、artifact 都由
两端 agent 各自汇报；Gateway 只做持久化、路由、合法性校验。详见
[ADR 002](./adr/002-gateway-as-hub.md) / [ADR 004](./adr/004-a2a-task-model.md)。

```
POST /v1/mesh/tasks
  # 发起方 agent 提交新 Task
  body: {
    task_id,                       # 客户端生成，UUID 或业务自定义
    context_id?,                   # 省略则 = task_id，新会话
    to_agent_id,                   # 被叫 agent
    message: {
      message_id,                  # A2A Message.messageId，UNIQUE 幂等
      parts: [{kind, text|raw|url|data, ...}]
    },
    metadata?
  }
  resp: 201 + {task_id, context_id, from_agent_id, to_agent_id, status: "submitted", ...}

  # 前置校验：
  #   - friendship.AreFriends(from, to) = true，否则 40302 不是好友
  #   - to_agent_id.kind != "virtual-user"（任务不反抛给用户）
  #   - message_id 已存在 → 幂等，返回已有 Task

POST /v1/mesh/tasks/{task_id}/messages
  # 追加 message 到 Task.history
  # role 由 Gateway 从 caller 身份自动推断：caller=from→user, caller=to→agent
  body: {message_id, parts, metadata?, reference_task_ids?}
  resp: 201 + Message 对象

  # 幂等：同 message_id 再提交，返回已有记录（内容相同时）
  # 终态 Task 拒绝 append（409）

POST /v1/mesh/tasks/{task_id}/artifacts
  # 追加 artifact。**仅被叫方 (to_agent_id 的 owner) 可调**
  body: {artifact_id, name?, description?, parts, metadata?}
  resp: 201 + Artifact 对象

  # (task_id, artifact_id) UNIQUE；同 id 重提返 40900 冲突
  # 同名 artifact 多行允许（版本）

POST /v1/mesh/tasks/{task_id}/transition
  # 推动状态机。谁能转到什么状态：
  #   - canceled：发起方 或 被叫方
  #   - submitted / working / input-required / auth-required / completed / failed / rejected：
  #     只有被叫方
  #   - input-required → submitted：发起方（补完消息后让被叫方继续）
  body: {to_state, status_message?, error?}
  resp: 200 + 更新后的 Task 对象

  # 非法转换返 40900 invalid_transition（如 completed → working）

GET /v1/mesh/tasks/{task_id}?include=history,artifacts
  # 获取 Task 详情；双方可读
  # include 可选 history / artifacts；省略则只返主表
  resp: 200 + Task 对象（按需带 history / artifacts）

GET /v1/mesh/tasks?context_id=ctx-xxx
  # 按 context 聚合；只返回调用方参与的 Task
  resp: 200 + {tasks: [...]}
```

**状态机**（A2A 对齐）：

```
submitted  ─▶  working  ─┬─▶  completed  (终态)
                          ├─▶  failed     (终态)
                          ├─▶  canceled   (终态)
                          ├─▶  input-required  ─┬─▶ submitted （发起方补消息）
                          │                    ├─▶ working
                          │                    └─▶ canceled
                          └─▶  auth-required   ─┬─▶ submitted
                                               └─▶ canceled
submitted  ─▶  canceled  (终态)
submitted  ─▶  rejected  (终态)
```

### Inbox（消息收件箱）

每个 agent 有一个 inbox；所有发给它的事件（message / artifact / transition）
先落 inbox，再尝试 HTTP push 送达。详见 [ADR 010](./adr/010-delivery-model.md)。

```
GET /v1/mesh/inbox?since={event_id}&limit={N}
  # agent JWT；拉 **自己** inbox 的事件
  # since=0 拉全部；通常 agent 本地保存上次 max_id，下次传入
  # limit 默认 100，上限 500
  resp: 200 + {
    events: [
      {
        id,                       # inbox event 自增 id，agent 按此去重
        kind,                     # message | artifact | transition
        task_id,
        ref_id,                   # message_id / artifact_id / to_state
        payload,                  # 对应结构体的完整 JSON
        created_at,
        delivered_at?             # push 成功时有（观测用，不影响 pull）
      }
    ],
    max_id                        # 下次 since 传这个
  }
```

**Push 送达**：agent 在 agents 表登记了 `url` 时，Gateway 会尝试 POST 到
`{url}/a2a/events`（body = 单个 event JSON）。成功则 MarkDelivered；失败
不重试（agent 下次 pull 会补齐）。

**去重**：同一 event 可能被 push 推来，也可能被 pull 拉到。Agent 侧按
event.id 去重即可（推荐保存 `last_processed_id`）。

### Gateway AgentCard（A2A spec 合规）

```
GET /.well-known/agent-card.json
  resp: {name, description, url, version, capabilities, skills: [...]}
  # skills 聚合所有 Active agent 的 skill
  # skill.id 格式: "{agent_id}/{skill_id}"
```

### Skill 自升级（GAS 用）

```
GET /v1/skill/version
  resp: {version, sha256}

GET /v1/skill/download
  resp: application/gzip tarball
```

## 错误响应

```json
{
  "code": 40003,
  "message": "not friend with target agent",
  "trace_id": "abc123"
}
```

### 错误码

| code | HTTP | 含义 | SDK 行为 |
|---|---|---|---|
| 40001 | 400 | 请求参数错误 | 改请求，不重试 |
| 40100 | 401 | 用户登录失败 / JWT 无效 | 登录页 |
| 40110 | 401 | agent JWT 过期 | **用 API Key 自动刷新，重试一次** |
| 40111 | 401 | agent JWT 或 API Key 格式错 / 不存在 | 不重试，通知用户 |
| 40112 | 401 | API Key 已被吊销 | 不重试，通知用户重新配置 |
| 40113 | 403 | /auth/token 里 agent_id 不属于该 key 的 owner | 不重试 |
| 40300 | 403 | 无权限 | 不重试 |
| 40301 | 403 | 不是 agent owner | 不重试 |
| 40302 | 403 | 不是好友 | 不重试 |
| 40400 | 404 | 资源不存在 | 不重试 |
| 40900 | 409 | 资源冲突（比如同名 agent_id）| 不重试 |
| 40901 | 409 | agent 已在别处上线 | 不重试 |
| 42900 | 429 | 限流 | 退避后重试 |
| 50000 | 500 | 服务端错误 | 可重试 |
| 50001 | 501 | 功能未上线 | 不重试 |
| 50301 | 503 | 目标 agent 离线 | 可重试（异步任务会重投） |
| 50302 | 503 | 熔断打开 | 退避后重试 |

## 版本兼容

- 新字段总是追加，不删字段
- 响应可能包含未声明字段，客户端应忽略
- 破坏性变更走 `/v2/`
