// Package middleware 提供 admin / mesh API 路由共用的 net/http 中间件。
// 这里的中间件不依赖具体 router 库，未来切换 chi / Gin 不用改策略。
package middleware

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/Ggrryta/agent-mesh/gateway/internal/api/httpx"
	"github.com/Ggrryta/agent-mesh/gateway/pkg/auth"

	jwtv5 "github.com/golang-jwt/jwt/v5"
)

// ctxKey 保持不导出，避免和其它包在 context 上碰撞。
// 读取走下面的类型化 helper。
type ctxKey int

const (
	ctxKeyClaims ctxKey = iota
)

// ClaimsFromContext 返回 RequireUser / RequireAgent 中间件放入 request
// context 的 auth claims；没有时返回 nil。
func ClaimsFromContext(ctx context.Context) *auth.Claims {
	c, _ := ctx.Value(ctxKeyClaims).(*auth.Claims)
	return c
}

// UIDFromContext 对只需要 uid 的 handler 的便捷封装。
func UIDFromContext(ctx context.Context) (int64, bool) {
	c := ClaimsFromContext(ctx)
	if c == nil {
		return 0, false
	}
	return c.UID, true
}

// RequireUser 用 JWT 保护 h，仅允许 user kind token 通过。
// 缺/坏 token → 401，kind 错 → 403。
func RequireUser(signer *auth.Signer, h http.Handler) http.Handler {
	return requireKind(signer, auth.KindUser, h)
}

// RequireAgent 用 JWT 保护 h，仅允许 agent kind token 通过。
// 更细粒度的 agent_id 与 path 匹配由路由层中间件做。
func RequireAgent(signer *auth.Signer, h http.Handler) http.Handler {
	return requireKind(signer, auth.KindAgent, h)
}

func requireKind(signer *auth.Signer, want auth.TokenKind, h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw := extractBearer(r)
		if raw == "" {
			writeAuthError(w, http.StatusUnauthorized, httpx.CodeTokenInvalid, "missing bearer token")
			return
		}
		claims, err := signer.Verify(raw)
		if err != nil {
			// JWT 过期走独立错误码（40110），让 SDK 能据此自动刷新重试。
			// 其它错误（格式 / 签名 / issuer 不对）统一 40111，不重试。
			if errors.Is(err, jwtv5.ErrTokenExpired) {
				writeAuthError(w, http.StatusUnauthorized, httpx.CodeTokenExpired, "token expired")
				return
			}
			writeAuthError(w, http.StatusUnauthorized, httpx.CodeTokenInvalid, "invalid token")
			return
		}
		if claims.Kind != want {
			writeAuthError(w, http.StatusForbidden, httpx.CodeForbidden, "wrong token kind")
			return
		}
		r = r.WithContext(context.WithValue(r.Context(), ctxKeyClaims, claims))
		h.ServeHTTP(w, r)
	})
}

// extractBearer 解析 Authorization: Bearer <token>。缺失或格式错返回 ""。
// WebSocket 场景下浏览器无法设自定义 header，fallback 到 ?token= query param。
func extractBearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if h != "" {
		parts := strings.SplitN(h, " ", 2)
		if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
			return strings.TrimSpace(parts[1])
		}
	}
	// Fallback: query param（仅 WebSocket 升级用，普通 API 不应走这条路径）
	if t := r.URL.Query().Get("token"); t != "" {
		return t
	}
	return ""
}

func writeAuthError(w http.ResponseWriter, status, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(httpx.ErrorBody{Code: code, Message: msg})
}
