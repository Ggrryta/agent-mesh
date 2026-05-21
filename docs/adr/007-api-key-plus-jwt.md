# ADR 007：用户持有 API Key，agent 用它换短期 JWT

- **状态**：Accepted
- **日期**：2026-05-12
- **替代**：Week 1 中 "创建 agent 直接返回 agent JWT" 的设计

## 背景

Week 1 的 agent 认证是：admin 创建 agent 时直接把一把 agent JWT 塞回给调用方
（GAS）。这把 JWT TTL 长、无法吊销、也无法轮换，只要泄露就是无限期风险。

新需求（业务层）：用户需要能**随时吊销**分发给某个 agent 的凭证，且每个
业务请求校验不能踩 DB（QPS 高）。

## 决策

用 **"长期凭证 + 短期凭证"** 的混合模型：

- **长期凭证 = API Key**，属于**用户**。用户在 Admin 控制台签发、复制、
  自行分发给名下的 agent（写 GAS 配置、塞 env、放 vault 等都行）。
- **短期凭证 = agent JWT**，TTL 默认 1h。agent 用 API Key 跟 Gateway 换，
  之后的业务请求只带 JWT，不再接触 api_keys 表。

**关键：一把 key 不绑定 agent**。用户可以把同一把 key 给多个 agent，也可以
为每个 agent 各签一把。Gateway 不关心"这把 key 分发给了谁"，只关心"这把
key 归哪个用户，那个用户有没有这个 agent"。

## 流程

```
（一次性）用户在 web 控制台 → POST /v1/admin/users/me/api-keys
                            → 响应里首次且唯一地看到 sk-am_xxx 原文
                            → 自己复制到 alice 和 bob 的 GAS 配置

（每次启动 / 每小时）agent → POST /v1/mesh/auth/token
                            Header: X-Api-Key: sk-am_xxx
                            Body:   {"agent_id": "alice-dev"}
                            响应:   {"token": "eyJ...", "expires_in": 3600}

（每次业务请求）agent → Authorization: Bearer <agent-jwt>
                      → middleware.RequireAgent 只验签名 + 过期
                      → 不查 DB、不查 Redis

（吊销时）用户 → DELETE /v1/admin/users/me/api-keys/{id}
              → api_keys.revoked_at = NOW()
              → 已签发的 JWT 继续活到自然过期（最多 1h）
              → agent 下次刷新 JWT 时被 401 拒绝
```

## 关键设计

### API Key 格式
`sk-am_<base64url(32 bytes)>`，约 50 字符。前缀 `sk-am` 让日志 / 代码一眼
可辨。32 字节随机 = 256 bit 熵，不可爆破。

### DB 存什么
只存 `SHA-256(raw)` 的 hex。**不用 bcrypt**：
- bcrypt ~60ms，对 /auth/token 路径过度保护
- SHA-256 <0.01ms，对业务请求无影响（业务请求走 JWT 根本不查）
- 256-bit 熵已经保证穷举不可行

### JWT Claims
`AgentClaims` 新增 `KeyID`（即 `api_key.id`）。好处：
- 审计：按 key 追查其签出的 JWT
- 将来若要做"吊销 key 时列黑名单 JTI"，信息已齐

不做当前不存在的功能：没有 Redis banlist、没有吊销即刻生效。TTL=1h 是
"吊销最坏生效时间"的可控上限。

### 谁能换到 JWT
`/auth/token` 校验两件事：
1. API Key 合法且未吊销（查 api_keys）
2. 请求 body 里的 `agent_id` 归属 key 的 owner（查 agents）

第二步 400 错误刻意统一成 "agent not owned by this api key"，不区分
"agent 不存在" vs "不是你的"，防止枚举攻击。

### SDK 契约
4 个错误码区分重试策略（见 docs/api.md）：

| code  | HTTP | 含义              | SDK 行为            |
|-------|------|-------------------|---------------------|
| 40110 | 401  | JWT 过期          | 自动刷新并重试一次  |
| 40111 | 401  | JWT/Key 格式错    | 不重试              |
| 40112 | 401  | Key 被吊销        | 通知用户，不重试    |
| 40113 | 403  | agent_id 不归你   | 不重试              |

## 考虑过的替代方案

### A. Agent 永久 JWT（Week 1 的现状）
- 优点：最简单
- 缺点：**不可吊销**、无法轮换、一旦泄露无限期风险
- 结论：不满足"随时吊销"的业务需求，淘汰

### B. 每请求都带 API Key
- 优点：吊销即刻生效
- 缺点：每请求查 DB，高 QPS 场景不可行
- 结论：性能不达标，淘汰

### C. API Key + Redis banlist + JTI 即刻吊销
- 优点：吊销即刻生效且性能 OK（Redis 查比 DB 便宜）
- 缺点：每请求多一次 Redis 查询，和 "业务请求零认证 DB" 初衷冲突；复杂度高
- 结论：MVP 不做，1h TTL 够用；如果将来用户真的需要"秒级吊销"，作为
  独立 opt-in middleware 加上即可（见"未来演进"）

### D. 创建 agent 时自动附赠 API Key
- 优点：首次体验一步到位
- 缺点：key 和 agent 的生命周期耦合，违背"key 属于用户"的心智
- 结论：不做。Agent 创建和 Key 签发显式解耦，UI 层可以做引导把两步
  串成一个向导

## 后果

### 得
- 业务请求零 DB 认证开销（只跑 HS256 签名校验，<1ms）
- /auth/token 才摸 api_keys，每 agent 每小时 ≤ 1 次，DB 压力可忽略
- 用户随时能吊销某把 key，最坏 1h 内生效
- Key 属于用户，UI 心智干净：用户管理 key，不用关心哪个 agent 用哪把

### 失
- 吊销不是即刻生效。业务可接受（紧急场景可手工改 DB 把所有
  JWT_AGENT_TTL 配置降到几秒钟重启 Gateway 就够）
- 多一张表、多一个 domain 包；相比"永久 JWT"增加了一些复杂度
- agent 启动需要一次 /auth/token 的额外网络交互（微不足道）

### 约束给下游
- **API Key 只能出现在 /auth/token 的 X-Api-Key 头上**。任何业务端点都
  不接受 API Key。这强制用户把 key 放在尽可能少曝光的位置。
- agent SDK（Week 4 做）负责自动刷新：JWT 剩余时间 < 10 分钟时主动刷，
  收到 40110 时自动重试一次。

## 未来演进

### 秒级吊销（optional middleware）
在业务路径前加一个可选 middleware：每请求 `EXISTS revoked:{kid}` in Redis。
吊销 key 时往 Redis `SADD` 对应 key_id（TTL=agentTTL）。启用后业务请求多
一次 Redis 查询 (~0.1ms)。生产 overlay 按需开。

### 多租户配额
`api_keys` 表加 `rate_limit` / `quota` 字段，每次 /auth/token 校验后把这些
塞进 JWT claims。业务中间件直接从 JWT 读配额，仍然零 DB。

### Agent 自动发现 key 泄露
agent SDK 发现自己 IP / UA 变化剧烈时主动 revoke + 重签，避免被盗后长期
无感使用。

## 参考

- docs/api.md §Auth
- 实现：internal/domain/apikey、internal/api/admin（Key 管理）、
  internal/api/mesh（/auth/token）
- migration：gateway/migrations/0002_api_keys.sql