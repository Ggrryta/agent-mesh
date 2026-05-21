package middleware

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"go.uber.org/zap"
)

// AccessLog 打每请求一行结构化日志。字段命名对齐 observability 约定：
//
//	method / path / status / bytes / duration_ms / client_ip / request_id /
//	uid / agent_id（JWT 已验时才有）
//
// 刻意不输出 query string —— 可能含 token / 敏感参数。路径模板由 handler
// 自行用 r.Pattern 记录（Go 1.22 http.ServeMux 支持）。
func AccessLog(log *zap.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// WebSocket 升级请求不 wrap ResponseWriter（Hijacker 接口需要透传）
			if isWebSocketUpgrade(r) {
				next.ServeHTTP(w, r)
				return
			}

			start := time.Now()
			rec := &responseRecorder{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(rec, r)

			// 探针路径太吵，降到 debug。默认只记业务请求的 info。
			lvl := zap.InfoLevel
			switch r.URL.Path {
			case "/healthz", "/readyz", "/startupz":
				lvl = zap.DebugLevel
			}

			fields := []zap.Field{
				zap.String("method", r.Method),
				zap.String("path", r.URL.Path),
				zap.Int("status", rec.status),
				zap.Int("bytes", rec.bytes),
				zap.Int64("duration_ms", time.Since(start).Milliseconds()),
				zap.String("client_ip", clientIP(r)),
				zap.String("request_id", RequestIDFromContext(r.Context())),
			}
			// 带上认证上下文（如果走了 auth middleware）。
			if c := ClaimsFromContext(r.Context()); c != nil {
				if c.UID != 0 {
					fields = append(fields, zap.Int64("uid", c.UID))
				}
				if c.AgentID != "" {
					fields = append(fields, zap.String("agent_id", c.AgentID))
				}
			}

			if ce := log.Check(lvl, "access"); ce != nil {
				ce.Write(fields...)
			}
		})
	}
}

// responseRecorder 记录状态码和响应字节数。不做 buffer，只做计数。
type responseRecorder struct {
	http.ResponseWriter
	status      int
	bytes       int
	wroteHeader bool
}

// Hijack 支持 WebSocket 升级：透传给底层 ResponseWriter。
func (r *responseRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if hj, ok := r.ResponseWriter.(http.Hijacker); ok {
		return hj.Hijack()
	}
	return nil, nil, fmt.Errorf("underlying ResponseWriter does not implement http.Hijacker")
}

func (r *responseRecorder) WriteHeader(code int) {
	if r.wroteHeader {
		return
	}
	r.wroteHeader = true
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *responseRecorder) Write(b []byte) (int, error) {
	if !r.wroteHeader {
		// 隐式 200；同步到状态码。
		r.wroteHeader = true
	}
	n, err := r.ResponseWriter.Write(b)
	r.bytes += n
	return n, err
}

// clientIP 优先 X-Forwarded-For 第一段（ingress / LB 透传）；没有则用 RemoteAddr。
// 刻意不做"信任链"校验 —— MVP 依赖 Ingress 清理 spoof，本层只记日志。
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		// 取第一个逗号前的片段。
		for i := 0; i < len(xff); i++ {
			if xff[i] == ',' {
				return trimSpace(xff[:i])
			}
		}
		return trimSpace(xff)
	}
	return r.RemoteAddr
}

// trimSpace 简化版 strings.TrimSpace，避免为一个小用途 import。
func trimSpace(s string) string {
	start, end := 0, len(s)
	for start < end && (s[start] == ' ' || s[start] == '\t') {
		start++
	}
	for end > start && (s[end-1] == ' ' || s[end-1] == '\t') {
		end--
	}
	return s[start:end]
}

func isWebSocketUpgrade(r *http.Request) bool {
	return strings.EqualFold(r.Header.Get("Connection"), "upgrade") &&
		strings.EqualFold(r.Header.Get("Upgrade"), "websocket")
}
