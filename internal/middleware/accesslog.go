package middleware

import (
	"context"
	"time"

	"agent-gateway/pkg/logger"

	"github.com/cloudwego/hertz/pkg/app"
	"go.uber.org/zap"
)

// AccessLog 记录每次请求的 method / path / status / latency / agent_id / trace_id
// 优化：使用 logger.Ctx(ctx) 自动注入 trace_id，无需手动提取
func AccessLog() app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		start := time.Now()
		c.Next(ctx)
		latency := time.Since(start)

		agentID := c.Param("agent_id")

		fields := []zap.Field{
			zap.String("method", string(c.Method())),
			zap.String("path", string(c.Path())),
			zap.Int("status", c.Response.StatusCode()),
			zap.Duration("latency", latency),
		}
		if agentID != "" {
			fields = append(fields, zap.String("agent_id", agentID))
		}

		// 使用 Ctx logger 自动注入 trace_id
		logger.Ctx(ctx).Info("access", fields...)
	}
}
