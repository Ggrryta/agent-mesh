# ADR 008：Friendship 模型（agent↔agent，owner 代管，virtual-user 隐式好友）

- **状态**：Accepted
- **日期**：2026-05-12
- **关联**：ADR 007（API Key + JWT）、concepts.md §Friendship

## 背景

mesh 内 agent 间的通信要有授权边界。候选模型：

- A. **user ↔ user**：两个用户互为好友后，名下所有 agent 互通
- B. **agent ↔ agent**：每个 agent 有独立社交圈
- C. **无 friendship**：mesh 全连通，靠其它机制（如黑名单）做限制

需求已确定：
- 业务层要求 **agent 粒度**（用户可能只想让 alice 和 bob 通信，不想让 carol 也自动连上）
- agent 不能自主加好友（否则可能产生意外的跨用户通信）
- virtual-user-* 是用户在 mesh 里的"任务入口"，不是社交节点

## 决策

### 1. 粒度 = agent ↔ agent

`friendships (from_agent_id, to_agent_id, status)`，UNIQUE KEY 在
`(from_agent_id, to_agent_id)`。每对 agent 独立一行，status 驱动。

### 2. 所有操作都是 owner 操作（不经 agent JWT）

- `POST /v1/admin/friends` 发起：body 带 `from_agent_id`，后端校验调用者是其 owner
- `POST /v1/admin/friends/{id}/accept|reject`：调用者必须是 `to_agent_id` 的 owner
- `POST /v1/admin/friends/{id}/revoke`：from 或 to 任一方的 owner 都可

mesh API 层**完全不暴露 friendship 端点**。agent 自身不参与社交管理。

**理由**：防止 agent 被别的 agent / 社工手段"自动加好友"；用户对自己 agent
的社交范围有完全控制权。

### 3. virtual-user-* 不参与显式 friendship

`Request` 校验：`from_agent_id` 或 `to_agent_id` 的 `kind = virtual-user` → 直接拒绝（`ErrVirtualUserPeer`，HTTP 400）。

virtual-user-* 和**同 owner 名下的 normal agent** 之间走 **隐式好友**：
`AreFriends()` 先做 ownership 配对判断，命中则直接返回 true，不查
`friendships` 表。

**理由**：
- virtual-user 是用户自己的 mesh 身份，和自己的 agent 之间不需要"加好友"
- 两个用户的 virtual-user 之间**不算**隐式好友（防止"用户间私聊"绕过 mesh 设计）
- 这个规则完全在 service 层的 `AreFriends`，schema 不动

### 4. 覆盖式重试

一对 `(from, to)` 只有一行，状态机：

```
        Request
     ┌──────────▶ pending ───Accept───▶ accepted
     │              │                       │
     │              │ Reject                │ Revoke
     │              ▼                       ▼
     │           rejected ◀─────Revoke──────┘
     │              │
     │              │ Request（同 pair）
     └──────────────┘
     ┌──────────────┐
     │              │ Request（同 pair）
     │              ▼
     │           revoked ────...
     │              │
     │              ▼
     └─── 覆盖回 pending，reason 覆盖
```

**关键边界**：
- `pending` 状态收到再次 Request → 返回 409 `ErrAlreadyPending`
- `accepted` 状态收到再次 Request → 返回 409 `ErrAlreadyAccepted`
  （要先 Revoke 才能重新走请求流程）
- `rejected` / `revoked` → UPDATE 同一行回 `pending`，覆盖 `reason`

**理由**：accepted 是用户已确认的关系；允许 "再 Request" 覆盖成 pending 会
误伤（比如 alice 再点一下加好友就意外断了原有关系）。rejected / revoked
则是"这段关系结束了"，允许用新 reason 重新尝试。

### 5. `AreFriends` 的职责边界

本方法是 Task 域（Week 3）发送前的"能不能送到"的唯一判据。

```go
AreFriends(a, b) bool :=
  a == b                                       ? false :    // 自己和自己不走 friendship
  (a, b) is virtual-user ↔ owner-normal pair   ? true  :    // 隐式
  ExistsAcceptedRow(a, b) or (b, a)            ? true  :    // 显式
  false
```

Task 域在 `POST /tasks` 时调用此方法；**friendship 本身不负责**判断"是否
允许给 virtual-user 打任务"（那是 Task 域的 `to_agent_id.kind != virtual-user`
校验）。

## 考虑过的替代

### 用户级 friendship（alice 的 owner 和 bob 的 owner 加好友，自动穿透）
- 优点：实现简单，只有一张 user_friendships 表
- 缺点：违背业务需求——用户要的是 per-agent 粒度。不接受

### 双方都 Request 才算好友（对称请求模型）
- 优点：更严格的"双向确认"
- 缺点：UX 差，业界通用都是 "发起+接受"。不采用

### 允许 accepted 被 Request 覆盖回 pending
- 优点：UI 一致，永远一个按钮"加好友"
- 缺点：误操作代价大，且没有业务价值
- 不采用。UI 层要在"已加好友"时把按钮隐掉，改为"解除好友"（Revoke）

## 后果

### 得
- agent 粒度的社交圈完全可控
- owner 代管避免了 agent 自主加好友带来的意外连通
- virtual-user 的"任务入口"定位清晰，不被社交污染
- 覆盖式 schema 简单：一对 agent 永远一行，状态机明确

### 失
- UI 多出"一个用户的多个 agent 各自管好友"的复杂度。Week 6 前端需要专门
  设计引导
- `AreFriends` 每次调用至少一次 `agents.Lookup` + 最多一次
  `friendships` 索引查询。Week 5 对热点场景加 cache

## 实现约束

- **domain/friendship 不依赖 domain/agent 包**：通过 `AgentLookup` 接口
  注入，具体实现由 `agent.NewLookupAdapter` 提供
- **mesh API 层没有 friendship 端点**：`/v1/mesh/*` 下 agent JWT 不能碰 friendship
- **AreFriends 是 Week 3 Task 校验的入口**：其它地方不要重复实现"朋友判定"逻辑
