package middleware

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/Ggrryta/agent-mesh/gateway/internal/api/httpx"
	"github.com/Ggrryta/agent-mesh/gateway/pkg/auth"
)

const signatureMaxAge = 60 // seconds

// TrustGateway 从 Gateway 注入的 X-Mesh-* header 中恢复 auth.Claims 到 context。
// 如果配置了 signKey，会验证 X-Mesh-Signature 防止绕过 Gateway 直连后端。
func TrustGateway(h http.Handler, opts ...TrustGatewayOption) http.Handler {
	cfg := &trustGatewayConfig{}
	for _, o := range opts {
		o(cfg)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		uidStr := r.Header.Get(HeaderMeshUID)
		if uidStr == "" {
			httpx.WriteError(w, http.StatusUnauthorized, httpx.CodeTokenInvalid, "missing identity header")
			return
		}

		uid, err := strconv.ParseInt(uidStr, 10, 64)
		if err != nil {
			httpx.WriteError(w, http.StatusUnauthorized, httpx.CodeTokenInvalid, "invalid uid header")
			return
		}

		agentID := r.Header.Get(HeaderMeshAgentID)
		ts := r.Header.Get(HeaderMeshTimestamp)
		sig := r.Header.Get(HeaderMeshSignature)

		if len(cfg.signKey) > 0 {
			if !VerifySignature(cfg.signKey, uidStr, agentID, ts, sig) {
				httpx.WriteError(w, http.StatusUnauthorized, httpx.CodeTokenInvalid, "invalid gateway signature")
				return
			}
			if tsInt, err := strconv.ParseInt(ts, 10, 64); err == nil {
				if abs(time.Now().Unix()-tsInt) > signatureMaxAge {
					httpx.WriteError(w, http.StatusUnauthorized, httpx.CodeTokenExpired, "gateway signature expired")
					return
				}
			}
		}

		claims := &auth.Claims{
			Kind:    auth.TokenKind(r.Header.Get(HeaderMeshTokenKind)),
			UID:     uid,
			AgentID: agentID,
		}
		if kidStr := r.Header.Get(HeaderMeshKeyID); kidStr != "" {
			claims.KeyID, _ = strconv.ParseInt(kidStr, 10, 64)
		}

		r = r.WithContext(context.WithValue(r.Context(), ctxKeyClaims, claims))
		h.ServeHTTP(w, r)
	})
}

type trustGatewayConfig struct {
	signKey []byte
}

type TrustGatewayOption func(*trustGatewayConfig)

// WithTrustSignKey 设置验签密钥。必须与 Gateway 的 WithSignKey 使用相同的密钥。
func WithTrustSignKey(key string) TrustGatewayOption {
	return func(c *trustGatewayConfig) {
		c.signKey = []byte(key)
	}
}

func abs(n int64) int64 {
	if n < 0 {
		return -n
	}
	return n
}
