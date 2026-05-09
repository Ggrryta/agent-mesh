package middleware

import (
	"context"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/google/uuid"
)

// RequestID 为每个请求生成唯一 ID 并写入响应头 X-Request-Id
// 如果请求头已包含 X-Request-Id，则复用该 ID
// 适用于没有全链路追踪的场景，或作为 trace_id 的补充
func RequestID() app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		// 优先使用请求头中的 Request ID
		requestID := string(c.GetHeader("X-Request-Id"))
		if requestID == "" {
			// 生成新的 Request ID
			requestID = uuid.New().String()
		}

		// 写入响应头
		c.Response.Header.Set("X-Request-Id", requestID)

		c.Next(ctx)
	}
}
