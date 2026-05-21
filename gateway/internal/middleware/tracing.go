package middleware

import (
	"net/http"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
)

var tracer = otel.Tracer("github.com/Ggrryta/agent-mesh/gateway")

// Tracing 为每个 HTTP 请求创建一个 span，并从 incoming headers 提取 trace context。
func Tracing(next http.Handler) http.Handler {
	prop := otel.GetTextMapPropagator()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := prop.Extract(r.Context(), propagation.HeaderCarrier(r.Header))
		ctx, span := tracer.Start(ctx, r.Method+" "+r.URL.Path,
			trace.WithSpanKind(trace.SpanKindServer),
			trace.WithAttributes(
				semconv.HTTPRequestMethodKey.String(r.Method),
				semconv.URLPath(r.URL.Path),
			),
		)
		defer span.End()

		// 把 trace_id 写到 response header，方便调试
		if span.SpanContext().HasTraceID() {
			w.Header().Set("X-Trace-Id", span.SpanContext().TraceID().String())
		}

		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
