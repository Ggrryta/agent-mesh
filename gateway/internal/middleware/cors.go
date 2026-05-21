// Package middleware：CORS 支持，给浏览器从 meshd 同源 fetch Gateway 用。
//
// 设计：
//   - 默认允许 http://127.0.0.1:* 和 http://localhost:* —— meshd 监听 loopback，
//     浏览器同源访问的请求会从这一类 origin 出发
//   - 其他 origin 不带 Access-Control-Allow-Origin，浏览器同源策略会自然拦
//   - 允许 credentials（如果将来需要 cookie 携带）
//   - 预检请求 (OPTIONS) 直接返 204

package middleware

import (
	"net/http"
	"strings"
)

// CORS 返回 net/http middleware：根据 Origin 决定是否回 CORS 响应头。
//
// extraAllowed 接受额外的精确 origin 字符串（部署到生产时给前端域用）。
// 留空也行——默认就允许 loopback。
func CORS(extraAllowed []string) func(http.Handler) http.Handler {
	allowed := map[string]bool{}
	for _, o := range extraAllowed {
		allowed[strings.TrimSpace(o)] = true
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			if origin != "" && originAllowed(origin, allowed) {
				h := w.Header()
				h.Set("Access-Control-Allow-Origin", origin)
				h.Set("Vary", "Origin")
				h.Set("Access-Control-Allow-Credentials", "true")
				h.Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-Api-Key")
				h.Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
				h.Set("Access-Control-Expose-Headers", "ETag")
				h.Set("Access-Control-Max-Age", "600")
			}
			if r.Method == http.MethodOptions && origin != "" {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// originAllowed 默认放行 loopback；额外 origin 走精确匹配。
func originAllowed(origin string, extra map[string]bool) bool {
	if extra[origin] {
		return true
	}
	// http://127.0.0.1:* / http://localhost:* / http://[::1]:*
	switch {
	case strings.HasPrefix(origin, "http://127.0.0.1:"),
		origin == "http://127.0.0.1",
		strings.HasPrefix(origin, "http://localhost:"),
		origin == "http://localhost",
		strings.HasPrefix(origin, "http://[::1]:"):
		return true
	}
	return false
}
