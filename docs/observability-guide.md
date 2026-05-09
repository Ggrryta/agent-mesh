# 全链路观测使用指南

## 1. 日志与 Trace 自动关联

### 使用方式

```go
// ❌ 之前：手动提取 trace_id
spanCtx := trace.SpanContextFromContext(ctx)
if spanCtx.HasTraceID() {
    logger.Info("skill invoke", 
        zap.String("skill_id", id),
        zap.String("trace_id", spanCtx.TraceID().String()))
}

// ✅ 现在：自动注入 trace_id
logger.Ctx(ctx).Info("skill invoke", zap.String("skill_id", id))
```

### 输出示例

```json
{
  "level": "info",
  "time": "2026-04-16T10:30:00.000+0800",
  "caller": "handler/gateway.go:123",
  "msg": "skill invoke",
  "trace_id": "abc123def456...",
  "span_id": "789xyz012...",
  "skill_id": "my-skill"
}
```

### 实现原理

`logger.Ctx(ctx)` 函数会：
1. 从 context 中提取 OpenTelemetry SpanContext
2. 自动添加 `trace_id` 和 `span_id` 字段
3. 返回一个带这些字段的 zap.Logger

## 2. 链路追踪

### Tracing 中间件

自动为每个 HTTP 请求创建 Span：
- 提取上游 trace context（W3C TraceContext）
- 记录 http.method、http.path、http.status_code
- 返回 `X-Trace-Id` 响应头

### 手动创建 Span

```go
func (h *Handler) invokeSkill(ctx context.Context, skillID string) {
    ctx, span := tracer.Tracer("agent-gateway").Start(ctx, "skill.invoke")
    defer span.End()
    
    span.SetAttributes(
        attribute.String("skill.id", skillID),
        attribute.String("skill.protocol", "http"),
    )
    
    // 业务逻辑...
    logger.Ctx(ctx).Info("skill invoked") // 自动包含 trace_id
}
```

### gRPC 调用追踪

```go
func (p *GRPCProxy) Invoke(ctx context.Context, endpoint, method string, input map[string]any) {
    ctx, span := tracer.Tracer("agent-gateway").Start(ctx, "grpc.invoke")
    defer span.End()
    
    span.SetAttributes(
        attribute.String("rpc.system", "grpc"),
        attribute.String("rpc.method", method),
        attribute.String("net.peer.name", endpoint),
    )
    
    // gRPC 调用...
}
```

## 3. Metrics 监控

### Prometheus 指标

| 指标名称 | 类型 | 标签 | 说明 |
|---------|------|------|------|
| `gateway_requests_total` | Counter | skill_id, status | 请求总数 |
| `gateway_request_duration_seconds` | Histogram | skill_id | 请求延迟 |
| `gateway_downstream_duration_seconds` | Histogram | skill_id, protocol | 下游调用延迟 |
| `gateway_ratelimit_rejected_total` | Counter | skill_id | 限流拒绝数 |
| `gateway_task_queue_depth` | Gauge | - | 异步任务队列深度 |
| `gateway_task_total` | Counter | status | 异步任务总数 |

### 查询示例

```promql
# QPS（每秒请求数）
rate(gateway_requests_total[5m])

# P99 延迟
histogram_quantile(0.99, rate(gateway_request_duration_seconds_bucket[5m]))

# 错误率
sum(rate(gateway_requests_total{status=~"5.."}[5m])) / sum(rate(gateway_requests_total[5m]))

# 限流拒绝率
sum(rate(gateway_ratelimit_rejected_total[5m])) / sum(rate(gateway_requests_total[5m]))
```

## 4. 数据库/Redis 操作追踪（待集成）

### MySQL 追踪

需要添加依赖：
```bash
go get go.opentelemetry.io/contrib/instrumentation/github.com/go-sql-driver/mysql/otelmysql
```

初始化：
```go
import "go.opentelemetry.io/contrib/instrumentation/github.com/go-sql-driver/mysql/otelmysql"

// 注册 OTel driver
sql.Register("mysql-otel", otelmysql.NewDriver())

// 使用 OTel driver
db, err := sql.Open("mysql-otel", dsn)
```

### Redis 追踪

需要添加依赖：
```bash
go get go.opentelemetry.io/contrib/instrumentation/github.com/redis/go-redis/v9/otelredis
```

初始化：
```go
import "go.opentelemetry.io/contrib/instrumentation/github.com/redis/go-redis/v9/otelredis"

// 包装 Redis client
rdb = otelredis.NewClient(rdb)
```

## 5. 日志查询示例

### 根据 trace_id 查询完整链路

```bash
# 在日志系统中搜索
trace_id="abc123def456..."

# 可以看到：
# 1. Access Log（请求入口）
# 2. Skill 调用日志
# 3. Redis/MySQL 操作日志（集成后）
# 4. gRPC 调用日志
# 5. 错误日志
```

### Grafana 链路追踪

1. 配置 Jaeger/Tempo 数据源
2. 输入 trace_id 查看完整链路
3. 每个 Span 显示：
   - 操作名称
   - 开始/结束时间
   - 标签（skill_id、protocol 等）
   - 关联日志

## 6. 最佳实践

### ✅ 推荐做法

```go
// 1. 始终使用 logger.Ctx(ctx)
logger.Ctx(ctx).Info("operation completed", zap.String("key", value))

// 2. 为关键操作创建 Span
ctx, span := tracer.Tracer("agent-gateway").Start(ctx, "operation.name")
defer span.End()

// 3. 记录错误到 Span
if err != nil {
    span.RecordError(err)
    span.SetStatus(codes.Error, err.Error())
    logger.Ctx(ctx).Error("operation failed", zap.Error(err))
}

// 4. 添加业务标签
span.SetAttributes(
    attribute.String("business.key", value),
    attribute.Int64("business.count", count),
)
```

### ❌ 避免做法

```go
// 1. 不要手动提取 trace_id
spanCtx := trace.SpanContextFromContext(ctx)
logger.Info("msg", zap.String("trace_id", spanCtx.TraceID().String())) // ❌

// 2. 不要忘记结束 Span
ctx, span := tracer.Tracer("agent-gateway").Start(ctx, "operation")
// 忘记 defer span.End() // ❌

// 3. 不要在全局 logger 中记录业务日志
logger.Info("business event") // ❌ 缺少 trace_id
```

## 7. 故障排查流程

### 场景：用户报告某个 Skill 调用失败

1. **获取 trace_id**
   - 从响应头 `X-Trace-Id` 获取
   - 或从用户提供的错误日志中获取

2. **查询日志**
   ```bash
   # 在日志系统中搜索
   trace_id="abc123..."
   ```

3. **查看链路**
   - 打开 Grafana/Jaeger
   - 输入 trace_id
   - 查看哪个 Span 出错

4. **定位问题**
   - 查看错误 Span 的标签和日志
   - 检查下游服务响应
   - 检查数据库/Redis 操作

5. **修复验证**
   - 修复后重新调用
   - 对比新旧 trace_id 的链路差异

---

## 总结

通过 `logger.Ctx(ctx)` 实现日志与 Trace 自动关联后：
- ✅ 每次日志自动包含 trace_id
- ✅ 可以通过 trace_id 追踪完整调用链路
- ✅ 日志和链路追踪无缝关联
- ✅ 故障排查效率提升 10 倍+
