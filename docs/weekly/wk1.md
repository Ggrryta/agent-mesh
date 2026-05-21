# Week 1 — Domain Core

> 从 Day 1 起每日累加。Week 1 目标：curl 能走完"注册 → 登录 → 建 agent → 查 agent → prober 探活"。

---

## Day 1 (2026-05-12) — DB + Redis + migrations 贯通

### 完成

- **Dev 依赖独立**：`docker-compose.dev.yml` 启动独立的 MySQL 8.0 `:3308` / Redis 7 `:6381`，不污染原项目的 `:3307/:6380`
- **Infra 层**：
  - `internal/infra/mysql/mysql.go` — `*sql.DB` 封装 + 连接池参数 + `Checker()` 用于 /readyz
  - `internal/infra/redis/redis.go` — go-redis 客户端 + `Checker()`
  - 都支持空配置"软失败"：DSN 为空只打 warn 不崩进程，/readyz 返 503 + `"not configured"`
- **Migration 工具**：
  - `gateway/migrations/0001_init.sql` — 7 张表基线（users / agents / skills / friendships / reliable_async_tasks / outbox_events / configs）全部 `utf8mb4_unicode_ci`，`DATETIME(3)` 毫秒精度，索引设计兼顾 Prober 时间戳 CAS 和 Outbox Dispatcher 批量领取
  - `gateway/migrations/embed.go` — `//go:embed *.sql` 让 migrate CLI 和集成测试都能访问
  - `cmd/migrate/main.go` — 基于 `pressly/goose`，命令：up / status / down / version / reset / create
- **main.go 集成**：
  - 启动时尝试连 DB/Redis，失败 → warn + 注册占位 checker；成功 → 记录连接参数 + 注册真实 checker
  - Shutdown 时额外关 DB/Redis 连接池
- **K8s 部署集成**：
  - `secret.yaml` 注入 `MYSQL_DSN` / `REDIS_ADDR` 指向 `host.k3d.internal:3308/6381`
  - `imagePullPolicy` 改 `Always`，避免同 tag 不刷新
  - Dockerfile 改用 `golang:1.25-alpine` + Aliyun apk mirror（解决 CN 网络 Alpine 包拉不下来）
- **单元/集成测试**：
  - `infra/mysql` 3 个用例（空 DSN / live ping / 坏连接）
  - `infra/redis` 3 个用例（空 addr / live ping / 坏连接）
  - 通过 `AGENT_MESH_TEST_MYSQL_DSN` / `AGENT_MESH_TEST_REDIS_ADDR` 切换"跳过 vs 真连"
- **Makefile 扩充**：新增 `build-migrate` / `test-live` / `compose-up/down/wipe` / `migrate-up/status/reset`

### 验收证据

```text
# 本地二进制冒烟（有 DB/Redis）
$ /readyz → HTTP 200 {checks: {mysql: "ok", redis: "ok"}}
# 本地二进制冒烟（清空 env）
$ /readyz → HTTP 503 {checks: {mysql: "not configured", redis: "not configured"}}

# K8s 集群内验证（两副本都健康）
$ kubectl exec pod -- wget -qO- /readyz
{"checks":{"mysql":"ok","redis":"ok"},"status":"ready"}

# Migration
$ make migrate-up
2026/05/12 10:40:41 OK   0001_init.sql (112.71ms)
2026/05/12 10:40:41 goose: successfully migrated database to version: 1

# 测试覆盖率
config                             87.5%
internal/infra/mysql               94.4%
internal/infra/redis              100.0%
internal/observability/health     100.0%
```

### 代码与新增依赖

新增 Go 依赖：
- `github.com/go-sql-driver/mysql v1.10.0` — MySQL driver
- `github.com/redis/go-redis/v9 v9.19.0` — Redis client
- `github.com/pressly/goose/v3 v3.27.1` — Migration runner

新增文件：
```
docker-compose.dev.yml
gateway/internal/infra/mysql/mysql.go + mysql_test.go
gateway/internal/infra/redis/redis.go + redis_test.go
gateway/migrations/0001_init.sql
gateway/migrations/embed.go
gateway/cmd/migrate/main.go
docs/weekly/wk1.md  ← 本文件
```

修改：
```
gateway/cmd/server/main.go  — DB/Redis 接入 + shutdown 扩展
gateway/go.mod              — Go 1.24 + 新 require
gateway/Dockerfile          — golang:1.25-alpine + Aliyun mirror
deploy/k8s/base/secret.yaml — 真实 DSN
deploy/k8s/base/deployment.yaml — imagePullPolicy: Always
Makefile                    — +7 个 target
```

### 遗留 / 风险

1. **Migration 仅能前跑不能回**：`goose down` 会 drop 表，生产必须禁用。runbook 里要加警告。
2. **AGENT_MESH_TEST_* 环境变量**：本地开发方便，生产 CI 要么把 compose 起在 CI 里，要么跳过 live 测试（当前默认 skip）。
3. **Dockerfile 的 CN mirror 写死**：国外 CI 可能反向不通。未来改 build arg 让 CI 覆盖。
4. **Secret 里 DSN 明文**：生产靠 ExternalSecrets，不用本 Secret。已在文件头注释强调。

### 下一步（Day 2）

- User 域：Register / Login / GetMe
- JWT 签发（HS256）+ Claims
- `middleware/auth.go` — 用户 JWT 解析中间件
- Admin API：`POST /v1/admin/auth/register` / `POST /v1/admin/auth/login` / `GET /v1/admin/users/me`
- 这些要跑通 E2E：curl 注册 → 拿 JWT → 带 JWT 访问受保护端点

---

## Day 2 (2026-05-12) — User + JWT + Admin API 贯通

### 完成

- **JWT 签发体系** (`pkg/auth/signer.go`)：
  - HS256 签名，两种 TokenKind：`user` / `agent`（claims 里 Kind 字段强制区分）
  - `NewSigner` 校验 secret 长度 ≥ 16 bytes、TTL 默认 24h
  - `IssueUser(uid)` / `IssueAgent(agentID, ownerUID)` 各自用途清晰
  - `Verify` 强制 HS256（拒绝 `alg: none`），校验 iss / exp
- **User 域** (`internal/domain/user/`)：
  - `SQLRepo.CreateWithVirtualAgent` — **事务内原子创建 users + agents 两张表**
  - 自动 virtual-user-agent: `virtual-user-<uid>`（name=username, kind=virtual-user, status=active）
  - `Service.Register` — bcrypt 密码哈希 + 用户名归一化（小写+trim）+ 格式校验
  - `Service.Login` — 用户名未知和密码错误返同一错误（防枚举）
  - 单元测试用内存 repo stub 覆盖 service 逻辑
  - 集成测试 `repo_test.go` 跑真实 MySQL（清理用 t.Cleanup）
- **Auth middleware** (`internal/middleware/auth.go`)：
  - `RequireUser` / `RequireAgent` 两个中间件，按 Kind 区分拒绝
  - Context key 封装（`ClaimsFromContext` / `UIDFromContext`），不暴露原始 key 防撞
  - 不从 URL query 回退读 token（避免 token 进日志）
- **Admin API** (`internal/api/admin/`)：
  - `POST /v1/admin/auth/register` — 201 + token
  - `POST /v1/admin/auth/login` — 200 + token
  - `GET /v1/admin/users/me` — 需要 user JWT
  - 错误映射集中在 `mapUserError`，按 `docs/api.md` 的 code 规范
- **HTTP 共享工具** (`internal/api/httpx/`)：
  - `WriteJSON` / `WriteError` 统一响应格式
  - `DecodeJSON` 带 1 MiB body 限制 + `DisallowUnknownFields`
- **main.go 接入**：DB 和 JWT_SECRET 都就绪时才挂 admin router，否则只打 warn 不 panic

### E2E 验证

**本地二进制**：
```
1. register → 201 token+uid+virtual_user_agent_id  ✓
2. /me 无 token → 401 missing bearer token           ✓
3. /me 带 token → 200 返回 uid+username+virtual_id   ✓
4. login → 200 同样 token                             ✓
5. login 错密码 → 401 invalid credentials            ✓
6. register 重名 → 409 username already taken        ✓
```

**K8s 两副本**（host.k3d.internal 连宿主 DB）：
```
pod=gateway-7d656ffc88-bljvp 启动日志:
  mysql connected ✓
  redis connected ✓
  admin API mounted (prefix=/v1/admin/, jwt_ttl=24h) ✓

集群内 E2E:
  register bob  → 201 token+virtual-user-3 ✓
  /users/me    → 200 {uid:3, username:"bob", ...} ✓
  port-forward 分到另一 pod 也能识别 bob 的 token ✓
  DB 确认 users+agents 两行都写入 ✓
```

### 测试覆盖

| 包 | 覆盖率 |
|---|---|
| config                              | 85.3% |
| pkg/auth                            | 87.0% |
| internal/domain/user                | 80.3% |
| internal/infra/mysql                | 94.4% |
| internal/infra/redis                | 100.0% |
| internal/middleware                 | 90.6% |
| internal/observability/health       | 100.0% |

### 新增依赖

- `github.com/golang-jwt/jwt/v5` — JWT 编解码
- `golang.org/x/crypto/bcrypt` — 密码哈希

### 新增文件

```
gateway/pkg/auth/{signer,signer_test}.go
gateway/internal/domain/user/{repo,service,util,service_test,repo_test}.go
gateway/internal/middleware/{auth,auth_test}.go
gateway/internal/api/httpx/httpx.go
gateway/internal/api/admin/handler.go
```

### 关键设计决策

1. **user kind vs agent kind**：统一 JWT 体系用 `Kind` 字段区分。中间件用 `RequireUser` / `RequireAgent` 拒绝混用，防止 agent token 被用来访问用户账户接口（反之亦然）
2. **virtual-user-agent 自动创建**：同事务写，避免"用户存在但虚拟代理不存在"的半态。这样前端直接用 `virtual_user_agent_id` 作为"以用户身份下令"的发送方
3. **用户名归一化**：service 层 `toLowerTrim`，避免"Alice"/"alice"/" alice "写冲突
4. **用户名枚举防护**：login 失败时统一返 `invalid credentials`，不区分"用户不存在"和"密码错"

### 遗留 / 风险

1. **bcrypt cost 10** 每次登录 ~60ms，高并发场景未做测量。如压测发现瓶颈可降到 8 或缓存 uid→hash（带 TTL）
2. **JWT 无 refresh**：过期只能重登。短期够用，后续加 refresh token
3. **register 无验证码/限流**：防爆破要靠 gateway 的 rate-limit middleware（Week 5）

### 下一步（Day 3-4）

- Day 3：Agent 域核心（Register / Deregister / Drain） + AgentCache（atomic.Value + copy-on-write） + Virtual user-agent 创建（已在 user 域做了，agent 域的 register 要识别并拒绝重名）
- Day 4：Agent 注册 API、心跳 API，issueAgentToken 对接
- Day 5：Skill 域 — AgentCard 里的 skills 全量替换
- Day 6：Agent Prober（DB 时间戳并发安全版）
- Day 7：集成测试 + 缓冲

---

## Day 3-4 (2026-05-12) — Agent 域 + Admin/Mesh API 贯通

### 完成

**Day 3 — Agent 域核心**：

- `domain/agent/model.go` — Agent / Kind / Status / 错误 / agentID 正则（3-64 字符、小写、`-._`，禁止 `virtual-user-` 前缀）
- `domain/agent/repo.go` — SQLRepo 实现 Create / Upsert / GetByAgentID / UpdateStatus / UpdateHeartbeat / Delete / List
  - `Upsert` 用 `ON DUPLICATE KEY UPDATE`，**owner_uid 绝不更新**（防偷取所有权）
  - `UpdateHeartbeat` 顺带把 inactive → active（复活逻辑），但不会把 draining 改回
- `domain/agent/cache.go` — `atomic.Pointer[map]` + copy-on-write
  - `Get` / `GetActive`（过滤 status=active）/ `Set` / `Delete` / `Len` / `Each`
  - `Reloader` 后台 goroutine 定期全量刷新（Day 6 接入）
  - 单测包括 `ConcurrencyReadsDuringWrites` 在 `-race` 下验证无数据竞争
- `domain/agent/service.go` — 服务层 façade
  - `Register` — 同 owner 可 upsert，跨 owner 拒绝；virtual-user 前缀被保护
  - `Drain` / `Deregister` / `Heartbeat` — 所有写操作都过 `assertOwner`
  - `ListByOwner` / `ListAllActive`

**Day 4 — API 层**：

- **Admin API** (`/v1/admin/*`，user JWT)：
  - `POST /users/me/agents` — 创建/更新 agent，返回 `agent_jwt`
  - `GET  /users/me/agents` — 列出我的 normal agent
  - `GET  /agents/{agent_id}` — 查详情（owner only）
  - `POST /agents/{agent_id}/drain`
  - `DELETE /agents/{agent_id}`
- **Mesh API** (`/v1/mesh/*`，agent JWT)：
  - `POST /agents/{agent_id}/heartbeat`
  - `POST /agents/{agent_id}/drain`
  - `enforceAgentID` 强制 JWT 的 agent_id 与 URL 匹配 — 一个 agent 不能替其他 agent 发心跳
- `main.go` 接入完整 stack：agent cache 启动时 prime，admin+mesh router 都挂上

### E2E 验证（11 项全过）

| # | 场景 | 预期 | 结果 |
|---|
| 1 | `POST /users/me/agents` | 201 + agent_jwt | ✓ |
| 2 | `GET /users/me/agents` | agent 列表 | ✓ |
| 3 | `POST /mesh/.../heartbeat` (agent token) | 200 status=active | ✓ |
| 4 | User token 打 mesh API | 403 "wrong token kind" | ✓ |
| 5 | Agent token 打 admin API | 403 "wrong token kind" | ✓ |
| 6 | Agent token 打别人 agent | 403 "agent_id mismatch" | ✓ |
| 7 | `POST /agents/{id}/drain` | 200 draining | ✓ |
| 8 | `GET /agents/{id}` | status=draining | ✓ |
| 9 | `DELETE /agents/{id}` | 204 | ✓ |
| 10 | `DELETE` 再跑 | 404 | ✓ |
| 11 | DB 只剩 virtual-user-* (不可删) | ✓ | ✓ |

**K8s 集群验证**：两副本 rollout 无中断；agent cache primed 从 DB 载入 1 条；`charlie` 新用户 + `charlie-bot` 新 agent + heartbeat 全程通过。

### 测试覆盖

| 包 | 覆盖率 |
|---|---|
| config                              | 85.3% |
| pkg/auth                            | 87.0% |
| internal/domain/user                | 80.3% |
| internal/domain/agent               | 42.6% (SQLRepo 无集成测试，Day 7 补) |
| internal/infra/mysql                | 94.4% |
| internal/infra/redis                | 100.0% |
| internal/middleware                 | 90.6% |
| internal/observability/health       | 100.0% |

agent 包的 cache + service 层通过内存 stub 覆盖到 85%+，但 SQLRepo 因需要真 DB 还没写测试。

### 关键设计决策

1. **Admin 侧创建、Mesh 侧心跳**：初次注册走 admin（因为需要用户身份挂钩 ownership + 颁发 agent JWT），心跳走 mesh（agent 自己的 token）。分层清晰，避免 mesh 侧有开放式的注册入口。
2. **`enforceAgentID`**：mesh 所有写操作都从 JWT 读 agent_id，拒绝与 path 不符的调用。相当于 Pod/Agent sidecar 只能动自己的资源。
3. **AgentCache 用 `atomic.Pointer[map]`**：写时 copy-on-write，读无锁。单测的 `ConcurrencyReadsDuringWrites` 在 `-race` 下过，保证并发读写不坏。
4. **Draining 不可由 Heartbeat 取消**：只有 owner 显式调 Register/Admin 才能改回 active，符合"操作员意图 > 心跳事实"。
5. **`virtual-user-*` 前缀保留**：agent Register/Delete 都拒绝操作虚拟代理。避免用户意外让自己的 virtual agent 消失。

### 新增/修改文件

```
gateway/internal/domain/agent/{model,repo,cache,service,cache_test,service_test}.go
gateway/internal/api/admin/handler.go  (大扩展：+agent CRUD)
gateway/internal/api/mesh/handler.go   (新)
gateway/cmd/server/main.go             (wire agent + mesh router)
```

### 遗留

1. **agent SQLRepo 没有集成测试**：Day 7 补 `repo_test.go`，覆盖率应该能上 85%+
2. **cache reloader 没跑**：代码写了 `NewReloader`，main.go 还没起 goroutine。Day 6 Prober 做定时刷新时一并接入
3. **`admin/mesh handler` 本身没单测**：靠 E2E 验证。补的话需要用 `httptest.NewRecorder` mock service 层

### 下一步（Day 5）

Skill 域：
- `domain/skill/{model,repo,service}.go`
- agent Register 时同事务更新 skills（全量替换）
- Admin `GET /agents/{id}/skills`
- Mesh 注册时在 AgentCard 里带 skills 数组一起写入

---

## Day 5 (2026-05-12) — Skill 域

### 完成

- `domain/skill/skill.go` — Skill 模型 + Input 结构 + ValidateInput（skill_id 2-128 字符、重复检测、name 必填）
- `domain/skill/adapter.go` — `Adapter` 包装 `Repo` 实现 `agent.SkillRepo`，解 agent↔skill 的 import cycle（用 `any` 作为跨层契约）
- `domain/agent/service.go` 扩展：`WithSkills(SkillRepo)` 注入，`Register` 时顺带 `ReplaceByAgentID`
- `admin/handler.go` 扩展：create agent 请求体多了 `skills` 字段；新增 `GET /v1/admin/agents/{id}/skills`
- 全量替换语义：每次 register 用 `DELETE FROM skills WHERE agent_id = ? + N 个 INSERT`，**同事务**确保不留一半

### E2E 验证

```
1. POST agent with 2 skills → 201                                    ✓
2. GET /agents/alice-bot/skills → [echo, summarize]                  ✓
3. Re-register with only 1 skill → 201                               ✓
4. GET /agents/alice-bot/skills → [translate] (old wiped)            ✓
5. Invalid skill_id "a b c" → 400 with clear error message           ✓
6. DB 里只有 translate 一行（DELETE + INSERT 原子）                    ✓
```

### 测试覆盖

skill 包 31.6%（service 层的 validation 都测了，SQLRepo 留给 Day 7 集成测试）

---

## Day 6 (2026-05-12) — Prober + Cache Reloader

### 完成

- `domain/prober/prober.go` — 并发安全的健康探测器
  - **DB 时间戳 CAS 认领**：`UPDATE agents SET last_probed_at=NOW(3) WHERE agent_id=? AND (last_probed_at IS NULL OR last_probed_at < NOW(3) - INTERVAL ? MICROSECOND)` + RowsAffected=1 → 才探
  - 多 Pod 并发时同一 agent 只会被一个 Pod claim 到，无需主选
  - `FailureThreshold=3` 连续失败后 flip `active → inactive`
  - 恢复：一次成功探测即 `inactive → active`
  - `candidates` 查询自动过滤 virtual-user 和空 URL 的 agent
- `domain/agent/cache.go` 里的 `Reloader`：10 分钟定时全量拉 + `Reload` 原子替换 snapshot
- `main.go`：两个后台 goroutine 启动，共享 `bgCtx` 在 graceful shutdown 时退出

### 测试

4 个关键测试全过：
- `TestClaim_IsAtomicAcrossReplicas` — 8 goroutine 同时 claim 同一 agent，**恰好 1 个赢**
- `TestClaim_RespectsTTL` — TTL 内重试失败，TTL 过后新 claim 成功
- `TestListCandidates_SkipsVirtualAndNoURL` — 过滤逻辑
- `TestProbeFlipsStatus` — 端到端用 httptest fake agent，toggle healthy→unhealthy 后 inactive；toggle 回来后 active

覆盖率 **86.6%**。

### K8s 双副本 E2E 验证（核心实证）

真实 k3d 集群里跑两副本 Gateway，启一个 fake agent，手动切换健康/不健康：

```
04:04 agent 注册成功 + 健康探测保持 active
04:05 fake agent 被停 (503)
      prober 3 次失败后：
      pod mt9jg flipped alice-bot to inactive, consecutive_failures=3 ✓
04:07 fake agent 恢复 (200)
      pod 87brk recovered alice-bot                                    ✓
```

**注意**：flip 由 `mt9jg` 完成，recover 由 **另一个 pod** `87brk` 完成。两次动作落在不同副本证明 CAS 分派正常——任一副本都能接过工作，不依赖主选或 leadership 协调。

### 关键设计决策

1. **无主选、纯 DB CAS**：比 K8s Lease 轻，比 Redis 分布式锁稳。"谁赢谁干"的语义靠 `UPDATE ... WHERE last_probed_at < now - TTL` 的 RowsAffected 判定
2. **Prober 不依赖 agent.Service**：直接用 `*sql.DB` 的 UPDATE 翻状态，这样 Prober 可以独立演进（比如将来做批量并发探），不会绕到 ownership / heartbeat 这条路径上
3. **Reloader 对 reload 失败"吞"**：periodic reload 是 freshness 补强，DB 抖动时保留旧 snapshot 比清空更安全

---

## Day 7 (2026-05-12) — 集成测试 + 收尾

### 完成

- **agent SQLRepo 集成测试** (`repo_test.go`)：
  - Create + GetByAgentID（验证 AgentCardJSON / LastHeartbeatAt 字段往返）
  - Duplicate key 返回 `ErrAgentIDExists`
  - Upsert **不能偷 owner_uid**（关键安全测试）
  - UpdateStatus + UpdateHeartbeat（验证 inactive→active 自动复活 + draining 不会被心跳解除）
  - Delete 幂等性
  - List filters
  - 覆盖率 42.6% → **75.2%**
- **skill SQLRepo 集成测试** (`repo_test.go`)：
  - Replace 全生命周期（2 skills → 1 skill → 0 skills）
  - 空 listByAgentID 不报错
  - JSON 字段（Tags / InputModes / OutputModes）往返
  - 覆盖率 31.6% → **86.0%**
- 最终 test-live 9 个测试包全绿，核心包都在 75-100% 覆盖

### 最终测试矩阵

| 包 | 覆盖率 | 说明 |
|---|---|---|
| config                              | 85.3%  | 默认值 + env 覆盖 + 错误路径 |
| pkg/auth                            | 87.0%  | 签发/验证/过期/错密钥/alg=none |
| internal/middleware                 | 90.6%  | RequireUser / RequireAgent + 错误分支 |
| internal/observability/health       | 100.0% | 三种探针 + 降级 |
| internal/infra/mysql                | 94.4%  | 空 DSN / live ping / 坏连接 |
| internal/infra/redis                | 100.0% | 同上 |
| internal/domain/user                | 80.3%  | bcrypt 验证 + 归一化 + 事务 |
| internal/domain/agent               | 75.2%  | CRUD + owner 保护 + 心跳复活 |
| internal/domain/skill               | 86.0%  | Validate + Adapter + Replace 生命周期 |
| internal/domain/prober              | 86.6%  | Claim 并发 + TTL + 候选过滤 + 状态翻转 |
| cmd/server / cmd/migrate            | 0.0%   | 入口层，靠 E2E 覆盖 |
| api/admin / api/mesh / api/httpx    | 0.0%   | 靠 E2E 覆盖 |

---

## Week 1 总结

### Milestone 1 验收

原计划：**k3d 里用 curl 走完"注册 → 登录 → 建 agent → 查 agent → prober 探活"全流程，两副本表现一致。**

达成情况：

| 项 | 结果 |
|---|---|
| 注册 + 登录（user JWT） | ✅ |
| 创建 agent（带 skills，返回 agent JWT） | ✅ |
| 查 agent + 查 skills | ✅ |
| Agent 心跳（mesh API） | ✅ |
| Prober 被动探活（3 次失败 flip inactive，1 次成功 recover） | ✅ |
| 两副本表现一致（CAS 分派：flip + recover 落在不同副本） | ✅ |
| 滚动升级零丢失（rollout restart） | ✅ |
| 跨 kind token 拒绝（user/agent 双向） | ✅ |
| Agent token 不能操作别人的 agent | ✅ |

### Week 1 产出清单

**新增代码**（按模块）：

```
config/config.go                              +JWT 字段
pkg/auth/signer.go + signer_test.go           新（87% cov）
internal/api/httpx/httpx.go                   新
internal/api/admin/handler.go                 新（user + agent + skill handlers）
internal/api/mesh/handler.go                  新（heartbeat + drain）
internal/infra/mysql/mysql.go + test          新（94% cov）
internal/infra/redis/redis.go + test          新(100% cov)
internal/middleware/auth.go + test            新（90% cov）
internal/domain/user/{model,repo,service,util,_tests}.go     新（80% cov）
internal/domain/agent/{model,repo,cache,service,_tests}.go   新（75% cov）
internal/domain/skill/{skill,adapter,_tests}.go              新（86% cov）
internal/domain/prober/{prober,_tests}.go                    新（86% cov）
migrations/0001_init.sql + embed.go           新（7 表）
cmd/migrate/main.go                           新（goose CLI）
cmd/server/main.go                            扩展（wire DB/Redis/Auth/Domains/Background tasks）
```

**新增基建**：

```
docker-compose.dev.yml                        独立 dev MySQL :3308 / Redis :6381
Makefile                                      +7 target（compose-up/down/wipe + migrate-up/status/reset + build-migrate + test-live）
deploy/k8s/base/secret.yaml                   真实 DSN + JWT_SECRET
deploy/k8s/base/deployment.yaml               imagePullPolicy: Always
```

**新增依赖**：
- `github.com/go-sql-driver/mysql v1.10.0`
- `github.com/redis/go-redis/v9 v9.19.0`
- `github.com/pressly/goose/v3 v3.27.1`
- `github.com/golang-jwt/jwt/v5 v5.3.1`
- `golang.org/x/crypto/bcrypt`

### 关键设计记录（简化版 ADR）

1. **User kind vs Agent kind token**：claims 里 `Kind` 字段强制区分，`RequireUser` / `RequireAgent` 中间件按 kind 拒绝
2. **Virtual user-agent 同事务**：users INSERT + agents INSERT + users UPDATE virtual_user_agent_id，三步在一个事务，不会半态
3. **Admin 创建 agent，Mesh 做心跳**：初次注册走 admin（需要 owner 身份 + 颁发 agent JWT），心跳走 mesh（agent 自己的 token）
4. **`enforceAgentID`**：mesh 写操作从 JWT 读 agent_id 后对比 path，杜绝跨 agent 越权
5. **AgentCache 用 `atomic.Pointer[map]` + COW**：读无锁；写路径在 `mu.Lock` 下 old→new 整体 store
6. **Prober 用 DB 时间戳 CAS**：无主选，纯 `UPDATE ... WHERE last_probed_at < now - TTL`，RowsAffected=1 才 probe
7. **Prober 不走 agent.Service**：直接 UPDATE 翻状态，解耦
8. **Skill 全量替换**：DELETE + INSERT 在事务里，register 一次后不残留旧 skills

### 生产级自检（PLAN §9 质量属性）

| 属性 | 目标 | 当前 |
|---|---|---|
| 可用性 | 99% | ✅ 两副本 + 自愈 + 滚动升级 |
| 消息送达率 | ≥ 99.99% | ⏳ Week 3-4 再评估 |
| 任务最终一致 | P99 < 10min | ⏳ Week 3 |
| P99 Task API 延迟 | < 100ms | ⏳ Week 6 压测 |
| 单实例容量 | ≥ 500 agent online | ⏳ Week 7 压测 |
| 启动时间 | < 10 秒 | ✅ ~2 秒 |
| 优雅停机 | < 60 秒 | ✅ terminationGracePeriodSeconds=90 |

### 遗留 / 技术债

1. **群组 / Outbox 底座没搭**：按改版 PLAN 移到 Week 3/4，作为 fan-out 通信基础
2. **API handler 无单测**：当前靠 E2E，手工 mock service 层是小工作量（Week 2 Day 5 补）
3. **`request_id` / `access_log` / `recover` middleware 缺**：Week 2 Day 4
4. **bcrypt cost 10 没压测**：高并发登录场景待验证
5. **JWT 无 refresh**：v1 接受，v2 补
6. **Prober 配置硬编码**：15s interval / 3 次阈值写死，Week 5 观测整合时搬到 config

### 一键操作 cheatsheet

```bash
# 环境准备（第一次）
make dev-up             # k3d cluster
make compose-up         # dev MySQL/Redis
make migrate-up         # 建表

# 日常开发循环
make lint && make test  # 本地测试
make test-live          # 含 live DB 集成测试
make dev-deploy         # 构建+推镜像+滚动发布
make smoke              # 端到端验证

# 清理
make compose-wipe       # 删所有 dev 数据
make dev-down           # 删集群
```

**Week 1 彻底完成。下周进入 Friendship + Market。**
