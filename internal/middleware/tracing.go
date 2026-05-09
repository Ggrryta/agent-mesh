package middleware

import (
	"context"

	"agent-gateway/pkg/tracer"

	"github.com/cloudwego/hertz/pkg/app"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

// hertzCarrier 将 Hertz RequestHeader 适配为 TextMapCarrier
type hertzCarrier struct {
	c *app.RequestContext
}

func (h hertzCarrier) Get(key string) string {
	return string(h.c.GetHeader(key))
}
func (h hertzCarrier) Set(key, val string) {
	h.c.Request.Header.Set(key, val)
}
func (h hertzCarrier) Keys() []string { return nil }

// Tracing 为每个请求创建 span，并将 trace_id 写入响应头 X-Trace-Id
func Tracing() app.HandlerFunc {
	prop := propagation.TraceContext{}
	tr := tracer.Tracer("agent-gateway")

	return func(ctx context.Context, c *app.RequestContext) {
		// 从上游请求头提取 trace context（支持 W3C traceparent）
		parentCtx := prop.Extract(ctx, hertzCarrier{c})

		spanName := string(c.Method()) + " " + string(c.Path())
		spanCtx, span := tr.Start(parentCtx, spanName,
			trace.WithSpanKind(trace.SpanKindServer),
			trace.WithAttributes(
				attribute.String("http.method", string(c.Method())),
				attribute.String("http.path", string(c.Path())),
			),
		)
		defer span.End()

		// 将 trace_id 写入响应头，方便 Consumer 关联日志
		traceID := span.SpanContext().TraceID().String()
		c.Response.Header.Set("X-Trace-Id", traceID)

		c.Next(spanCtx)

		span.SetAttributes(attribute.Int("http.status_code", c.Response.StatusCode()))
	}
}
