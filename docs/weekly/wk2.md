# Week 2 — Friendship + Market + 通用中间件

> 2026-05-12 ~ 2026-05-13。Week 2 主体完成。

## 已交付（按 Day）

### Day 1-2 Friendship 域
详见独立 wk2-friendship.md。核心：`domain/friendship`、6 个 Admin 端点、覆盖率 80.9%、E2E 16 个场景全通、ADR 008。

### Day 3 Market
- `agent.Repo.Filter` 扩展 `Search` / `Offset` + `escapeLike`
- `agent.Service.ListMarket`：仅 kind=normal + status=active
- `skill.Repo.ListByAgentIDs`：批量查避免 N+1
- Admin `GET /v1/admin/market/agents`：分页 + 搜索 + 脱敏（**不返** owner_uid / url / version）

### Day 4 中间件
三件套：`internal/middleware/` 新增
- `request_id.go`：`WithRequestID`，32 hex id；客户端传入会被复用（长度校验防滥用）
- `access_log.go`：结构化日志（method / path / status / bytes / duration_ms / client_ip / request_id / uid / agent_id），探针路径降到 debug
- `recover.go`：捕获 panic 返 500 + 标准 ErrorBody，保留 `http.ErrAbortHandler` 传播

挂载时序：`request_id → recover → access_log → mux`（主 main.go 里）。

### Day 5 Handler 层单测
用**内存 repo + 真实 service**的"集成性单测"——比纯 mock 真实，比 live DB 快：
- `internal/api/admin/handler_test.go`：19 个 handler 全路径 + 错误路径，**覆盖率 75.3%**
- `internal/api/mesh/handler_test.go`：`/auth/token` 4 个错误码分支 + heartbeat / drain，**覆盖率 69.3%**（PLAN 要求 ≥70%，差 0.7%，受限于那些纯 pass-through handler）
- `internal/middleware/middleware_extras_test.go`：request_id / access_log / recover 9 条测试，middleware 包覆盖 **79.4%**

### Day 6 压测基线
- `loadtest/register_login.js` k6 脚本（bcrypt 重路径）
- `loadtest/heartbeat.js` k6 脚本（JWT + DB UPDATE 高频路径）
- `docs/weekly/wk2-baseline.md` 基线报告模版（待跑数填表）
- `Makefile` 新增 `load-register` / `load-heartbeat` 目标

Day 7 缓冲未使用。

## 指标汇总

| 包 | 覆盖率 | 备注 |
|---|---|---|
| config | 83.3% | |
| pkg/auth | 92.3% | |
| internal/middleware | 79.4% | +三件新中间件 |
| internal/api/admin | **75.3%** | Week 1 时 0%，重点提升 |
| internal/api/mesh | **69.3%** | 同上 |
| internal/infra/mysql | 94.4% | |
| internal/infra/redis | 100.0% | |
| internal/domain/user | 80.3% | |
| internal/domain/agent | 70.7% | |
| internal/domain/apikey | 86.6% | |
| internal/domain/friendship | **80.9%** | 本周新增 |
| internal/domain/prober | 90.2% | |
| internal/domain/skill | 59.0% | 下降：新加 ListByAgentIDs 未补 live 测试 |
| internal/observability/health | 100.0% | |

## E2E 验证

Market + 中间件在 K8s 2 副本集群上跑通：
- Market 列表、搜索、分页全正确
- **无 url / owner_uid 泄露**（脱敏正确）
- **无 virtual-user 在 market 出现**
- 未授权 401
- X-Request-Id 生成 + 复用 incoming id

## 关键设计决策（供后续 week 参考）

1. **Handler 单测走"真实 service + 内存 repo"**：避免 mock 的脆性，同时不依赖 live DB。
   mem*Repo 实现放在 handler_test.go 同包，不污染生产代码。

2. **Market 脱敏清单**：`marketAgentResp` 刻意**不含**：
   - `owner_uid`（避免按 owner 聚合扫描）
   - `url`（agent 入站地址，对外泄露会直接被打）
   - `version` / `last_heartbeat_at`（信息噪声）
   - 等 Week 6 前端做 Market 页时如需上线状态，单独加 `online` 布尔字段，别直接暴露 `last_heartbeat_at`

3. **`escapeLike` 转义 % / _ / \\**：防止客户端用 `%` 作为通配符把全表拉回。

4. **中间件 request_id 复用 incoming**：和 ingress-nginx / envoy 默认约定对齐，方便端到端追链。
   长度 >128 拒绝，防滥用。

5. **access_log 的探针降级**：/healthz / /readyz / /startupz 在 info 级别不输出，避免刷日志。
   debug 级别可以看到，用于调试 K8s probe 时序。

6. **Recover 的 ErrAbortHandler 例外**：net/http 用它作为"中止写响应"的机制；Recover 必须让它继续冒泡，否则会吞掉服务端自己的 abort 逻辑。

## 遗留问题

- **skill 包 live 测试下降到 59%**：新加的 `ListByAgentIDs` 只在 handler 层被 E2E 打到；单独的 SQLRepo live test 补一下才能回到 86%。优先级低，Week 3 顺手做
- **wk2-baseline.md 的数字未填**：需要本地跑 k6 后录入。Week 5 限流上线前要有基线
- **mesh handler 覆盖率 69.3% 差 0.7% 未过门槛**：主要是 `handleDrain` 的错误分支；Week 3 做 Task 时顺手加
- **admin / mesh 没过 `make lint`（skill stubRepo 缺 method）**：已修

## 下一步（Week 3）

按 PLAN Week 3 是 **Reliable Task（扫表版，A2A 对齐）**，5 天 + 2 天缓冲。关键交付：
- Day 1-2：Task + TaskMessage + TaskArtifact 三张表的 repo + service
- Day 3：`POST /v1/mesh/tasks` + `AreFriends` 调用（Week 2 Friendship 接入点）
- Day 4：Worker 扫表 + CAS claim + 状态机
- Day 5：input-required 续约 + 失败重试 + 僵尸回收
- Day 6-7：E2E + 缓冲

Week 3 要兑现的 Friendship 承诺：
- Task 发送前必须过 `AreFriends`（`from_agent_id`, `to_agent_id`）
- virtual-user 不能作为 `to_agent_id`（"不反抛任务给用户"的实现点）
