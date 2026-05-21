# Week 2 — Friendship 域（Day 1-2）

> 2026-05-12。按原 PLAN 这是 Week 2 主干的第一个交付物。Week 2 剩下的 Day 3-7
> （Market + middleware 补齐 + handler 单测 + 压测基线 + 缓冲）接着做。

## 目标

MVP 的 agent-to-agent 授权链路。粒度 = agent ↔ agent，所有操作由 owner
在 Admin API 里进行，virtual-user-* 走隐式好友。详见 ADR 008。

## 完成项

### 数据层
- `friendships` 表 schema 在 `0001_init.sql` 里已经建好（含 `uk_pair` 唯一索引），本次无新增 migration

### 域层：`internal/domain/friendship/`
- `model.go`：`Status` 枚举（pending/accepted/rejected/revoked）、`Friendship` 结构体、8 个 sentinel 错误
- `repo.go` / `SQLRepo`：7 个方法
  - `GetByPair / GetByID / Insert / UpdateToPending / UpdateStatus / ListInvolvingAgent / ListIncomingPending / ExistsAccepted`
  - `UpdateToPending` 仅在 `rejected/revoked` 状态生效（SQL 层 WHERE 兜底，service 不用二次判断）
  - `UpdateStatus` 带 `fromStatus` 校验，防止并发踩旧状态
- `service.go` / `Service`：完整业务规则封装
  - `Request`：从 agent 的 owner 发起，状态机 + 覆盖式重试
  - `Accept / Reject / Revoke`：权限规则内聚在 `transitOp` 结构里，三个操作共用 `transition` 实现
  - `ListFriends / ListIncomingPending`：owner 权限校验
  - `AreFriends`：Task 域（Week 3）授权检查的唯一入口，含 **virtual-user 隐式好友**
- `AgentLookup` 接口 + `agent.NewLookupAdapter`：规避 domain/friendship → domain/agent 的循环依赖

### API 层：`internal/api/admin/handler.go`
6 个新端点：
- `POST   /v1/admin/friends` — 发起请求
- `GET    /v1/admin/agents/{id}/friends?status=…` — 查某 agent 的朋友
- `GET    /v1/admin/agents/{id}/friends/incoming` — 查 pending 的收件人列表
- `POST   /v1/admin/friends/{id}/accept`
- `POST   /v1/admin/friends/{id}/reject`
- `POST   /v1/admin/friends/{id}/revoke`

`mapFriendError` 把 8 个域错误按 400 / 403 / 404 / 409 分类映射。

### 装配
- `cmd/server/main.go`：`friendship.NewService` 用 `agent.NewLookupAdapter(agentSvc)` 注入
- `admin.New` 新增 `friends *friendship.Service` 参数

### 文档
- `docs/adr/008-friendship-model.md` — 决策记录（粒度 / owner 代管 / virtual-user 隐式 / 覆盖式重试等 5 条决策）
- `docs/adr/README.md` — 索引更新
- `docs/api.md` — Friendship 段落重写，覆盖全部端点 + 语义 + 错误码

## 测试

### 单测（memRepo + memAgents）
`service_test.go` 覆盖 16 条业务规则，每条对应一个测试函数：
- Request：happy path / self / not_owner / virtual-user on either side / unknown agent / dup pending / dup accepted / cover rejected / cover revoked
- Accept / Reject / Revoke：仅接收方可 / invalid_transition / anySide 允许双方撤销 / 仅 accepted 可 revoke
- AreFriends：virtual-user 隐式 / 两 virtual 之间不隐式 / accepted 双向 / revoke 后失效
- List：owner 校验 / status 过滤 / incoming 排除非 pending

### Live 集成测试（MySQL）
`repo_test.go` 7 条：
- Insert + Get 回路
- uk_pair 唯一约束
- UpdateToPending 仅 terminal 生效
- UpdateStatus 带 fromStatus 校验并发安全
- ListInvolving / ListIncomingPending
- ExistsAccepted 双向查询对称

覆盖率：**friendship 包 80.9%**。

### E2E（K8s 集群 2 副本）
16 个场景全通，覆盖：
- pending → accepted → revoked → 重新 Request → rejected 全状态机
- 所有错误路径（40001 / 40301 / 40900 各触发）
- 跨 owner 查询拒绝
- virtual-user 拒绝
- owner 校验在 accept / reject / revoke 各端点生效

## 关键工程决策

1. **AgentLookup 接口 + Adapter 模式**：friendship 不直接 import agent 包；
   agent 提供 `NewLookupAdapter` 暴露最小接口 `(ownerUID, kind, found)`。
   kind 用字符串传（非类型常量）—— 跨包串一个 enum 会引来更多依赖。

2. **transitOp 内聚三种状态转移**：Accept / Reject / Revoke 只差
   (fromStatus, toStatus, ownerSide) 三元组，用一个 `transition` 方法处理，
   避免重复的 ownership 校验代码。

3. **UpdateToPending 的 WHERE 兜底**：service 层先查状态再更新不是严格安全
   （并发下可能旧状态被别处改）。SQL 层加 `AND status IN (rejected, revoked)`
   让覆盖操作"无论并发与否都不会把 accepted 误伤回 pending"。

4. **Reason 覆盖式，不保留历史**：MVP 选择简单 —— 每次 UPDATE 直接覆盖旧
   reason。审计如需历史，由日志层提供。

5. **ErrInvalidTransition 映射成 409 而不是 400**：状态机拒绝本质是"资源
   在此状态下不能做这件事"—— 和 already_pending / already_accepted 语义
   类似，都是 409。400 留给"请求本身格式错"。

## 指标

| 包 | 覆盖率 |
|---|---|
| internal/domain/friendship | 80.9% |

其它包不变。全部 12 个测试包绿。

## 下一步（Week 2 Day 3+）

- **Day 3**：Market 端点 `GET /v1/admin/market/agents`（过滤 virtual-user）
- **Day 4**：通用中间件 `request_id` / `access_log` / `recover`
- **Day 5**：handler 层单测补齐（目前 admin / mesh 都靠 E2E，不合规）
- **Day 6**：压测基线准备（k6 脚本 + 容量基线报告框架）
- **Day 7**：缓冲

Friendship 域本身**可能要在 Week 3 做 Task 时回头调整**：`AreFriends` 的
热点场景 caching。但接口稳定了，不会影响 Task 域的落地。
