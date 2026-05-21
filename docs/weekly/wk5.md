# Week 5 — 治理层 + 观测 + 群组底座

> 2026-05-13。Week 5 主体完成。

## 范围调整

PLAN 原版 Week 5 包含 FeedHub，但已在 Week 4 完成。本周重新排优先级：
**可观测 → 治理 → 群组**。

## 已交付

### Day 1：Prometheus Metrics

`internal/observability/metrics/metrics.go` — 四组指标：

| 组 | 指标 | 说明 |
|---|---|---|
| HTTP | `mesh_http_requests_total` | 按 method/path/status 计数 |
| HTTP | `mesh_http_request_duration_seconds` | 延迟直方图 |
| Task | `mesh_task_created_total` / `messages_total` / `artifacts_total` / `transitions_total` | 业务计数 |
| Inbox | `mesh_inbox_enqueue_total` / `pull_total` / `pull_events_returned` / `push_success_total` / `push_fail_total` | 投递指标 |
| Feed | `mesh_feed_publish_total` / `active_subscribers` | WebSocket 指标 |
| Agent | `mesh_agent_cache_size` / `heartbeat_total` | 在线态 |

`internal/middleware/metrics.go` — HTTP 请求 QPS + 延迟中间件。

### Day 2：OTel Tracing

- `internal/observability/tracer/tracer.go`：TracerProvider 初始化（MVP noop exporter，trace_id 仍生成）
- `internal/middleware/tracing.go`：为每个请求创建 span + 从 header 提取 trace context + 响应头写 `X-Trace-Id`
- 中间件链：`RequestID → Recover → Tracing → Metrics → AccessLog → Mux`

### Day 3：限流

`internal/ratelimit/limiter.go`：
- Token bucket 算法，per-agent 隔离
- 默认 50 req/s，突发 100
- `Middleware()` 方法返回 http.Handler 中间件
- 被限流时返回 429 + `mesh_ratelimit_reject_total` 计数
- 4 个单测

### Day 4：熔断

`internal/circuitbreaker/guard.go`：
- 基于 `sony/gobreaker`，per-agent 懒初始化
- 默认 5 次连续失败打开，30s 后半开
- `Execute(agentID, fn)` 保护下游调用
- `mesh_breaker_state` gauge 按 agent 上报状态
- 3 个单测

### Day 5：群组模型

`internal/domain/group/`：
- `model.go`：Group / Member / MemberRole
- `repo.go`：Repo 接口（CreateGroup / AddMember / RemoveMember / ListMembers / IsMember）
- `service.go`：业务逻辑（owner 权限校验）
- 3 个单测

`migrations/0004_groups_outbox.sql`：
- `groups` 表（group_id / context_id / name / owner_uid）
- `group_members` 表（group_id / agent_id / role）
- `outbox_events` 表（event_type / payload / status / retries）

### Day 6：Outbox + Dispatcher

`internal/domain/outbox/`：
- `model.go`：Event / Status / Repo 接口
- `dispatcher.go`：每秒扫 pending 事件，分发给 handler，失败指数退避重试（最多 10 次）
- `Publish()` 便捷函数写 outbox
- 3 个单测（处理+标记sent / 失败重试 / publish）

## 测试汇总

23 个包全部通过，0 失败。新增 13 个单测。

## 新增依赖

- `go.opentelemetry.io/otel` + `otel/sdk` + `otel/trace` + `otel/propagation`
- `github.com/sony/gobreaker`

## 代码量

新增约 **900 行**（metrics 120 + tracing 80 + ratelimit 130 + breaker 100 + group 200 + outbox 170 + tests 100）

## 遗留

- 限流/熔断未接入 main.go（接口已就绪，Week 6 挂载到 mesh mux）
- 群组 API handler 未写（domain 层就绪，handler 留 Week 6）
- Outbox dispatcher 未接入 main.go（handler 回调需要群组 fan-out 逻辑）
- OTel exporter 是 noop（生产接 OTLP 时改 tracer.Init 参数）
- Grafana dashboard JSON 未写（需要实际跑起来调）

## 下一步（Week 6）

PLAN 原版 Week 6 是前端控制台。按当前进度建议：
1. 群组 API handler（admin + mesh）
2. Outbox dispatcher 接入 + 群消息 fan-out handler
3. 限流/熔断挂载到 mesh mux
4. 前端控制台（如果时间允许）
