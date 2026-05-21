package middleware

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
)

// RequestIDHeader 是 client / 上游 LB 传入 / 下游响应回写的 Header 名。
// 和业界约定保持一致（k8s ingress-nginx / envoy 默认），方便端到端排查。
const RequestIDHeader = "X-Request-Id"

// ctxKeyRequestID 给同一个 context 里附带一个 request id；handler 层读它写日志。
type ctxKeyRequestID struct{}

// NewRequestID 生成 16 字节随机 id 的 hex（32 字符）。
// 不保证全局唯一 —— 但碰撞几率低到可以忽略，且排查场景不需要保证。
func NewRequestID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b) // 失败极罕见；失败时返回空 hex 也不致命
	return hex.EncodeToString(b)
}

// RequestIDFromContext 从 context 读当前请求的 request id。没有则返回空串。
func RequestIDFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(ctxKeyRequestID{}).(string); ok {
		return v
	}
	return ""
}

// WithRequestID 注入 request id，供 access log / handler 使用。
// 优先使用 client 传入的 X-Request-Id（便于跨服务追链），没有才生成新的。
// 响应头必定回写，让客户端能把它记到自己的日志里。
func WithRequestID(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rid := r.Header.Get(RequestIDHeader)
		if rid == "" || len(rid) > 128 {
			// 刻意限长，避免客户端用超长串撑爆日志。
			rid = NewRequestID()
		}
		w.Header().Set(RequestIDHeader, rid)
		ctx := context.WithValue(r.Context(), ctxKeyRequestID{}, rid)
		h.ServeHTTP(w, r.WithContext(ctx))
	})
}
