package middleware

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Ggrryta/agent-mesh/gateway/internal/api/httpx"
	"github.com/Ggrryta/agent-mesh/gateway/pkg/auth"

	jwtv5 "github.com/golang-jwt/jwt/v5"
)

const (
	HeaderMeshUID       = "X-Mesh-UID"
	HeaderMeshAgentID   = "X-Mesh-Agent-ID"
	HeaderMeshTokenKind = "X-Mesh-Token-Kind"
	HeaderMeshKeyID     = "X-Mesh-Key-ID"
	HeaderMeshSignature = "X-Mesh-Signature"
	HeaderMeshTimestamp = "X-Mesh-Timestamp"
)

// GatewayAuth 在 API Gateway 层统一完成 JWT 验证并注入身份 header。
// 公开端点（/auth/*）直接放行，其余按路径前缀要求对应的 token kind。
// signKey 用于生成 X-Mesh-Signature，后端通过 TrustGateway 验签防止直连绕过。
func GatewayAuth(signer *auth.Signer, opts ...GatewayAuthOption) func(http.Handler) http.Handler {
	cfg := &gatewayAuthConfig{}
	for _, o := range opts {
		o(cfg)
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			stripHeaders(r)

			if isPublicPath(r.URL.Path) {
				next.ServeHTTP(w, r)
				return
			}

			raw := extractBearer(r)
			if raw == "" {
				writeAuthError(w, http.StatusUnauthorized, httpx.CodeTokenInvalid, "missing bearer token")
				return
			}

			claims, err := signer.Verify(raw)
			if err != nil {
				if errors.Is(err, jwtv5.ErrTokenExpired) {
					writeAuthError(w, http.StatusUnauthorized, httpx.CodeTokenExpired, "token expired")
					return
				}
				writeAuthError(w, http.StatusUnauthorized, httpx.CodeTokenInvalid, "invalid token")
				return
			}

			want := requiredKind(r.URL.Path)
			if want != "" && claims.Kind != want {
				writeAuthError(w, http.StatusForbidden, httpx.CodeForbidden, "wrong token kind")
				return
			}

			injectHeaders(r, claims, cfg.signKey)
			next.ServeHTTP(w, r)
		})
	}
}

type gatewayAuthConfig struct {
	signKey []byte
}

type GatewayAuthOption func(*gatewayAuthConfig)

// WithSignKey 设置 Gateway 签名密钥。设置后 Gateway 会在转发时附加 HMAC 签名，
// 后端 TrustGateway 验签以确保请求确实经过 Gateway。
func WithSignKey(key string) GatewayAuthOption {
	return func(c *gatewayAuthConfig) {
		c.signKey = []byte(key)
	}
}

func stripHeaders(r *http.Request) {
	r.Header.Del(HeaderMeshUID)
	r.Header.Del(HeaderMeshAgentID)
	r.Header.Del(HeaderMeshTokenKind)
	r.Header.Del(HeaderMeshKeyID)
	r.Header.Del(HeaderMeshSignature)
	r.Header.Del(HeaderMeshTimestamp)
}

func injectHeaders(r *http.Request, c *auth.Claims, signKey []byte) {
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	r.Header.Set(HeaderMeshUID, strconv.FormatInt(c.UID, 10))
	r.Header.Set(HeaderMeshTokenKind, string(c.Kind))
	r.Header.Set(HeaderMeshTimestamp, ts)
	if c.AgentID != "" {
		r.Header.Set(HeaderMeshAgentID, c.AgentID)
	}
	if c.KeyID != 0 {
		r.Header.Set(HeaderMeshKeyID, strconv.FormatInt(c.KeyID, 10))
	}
	if len(signKey) > 0 {
		sig := computeSignature(signKey, r.Header.Get(HeaderMeshUID), c.AgentID, ts)
		r.Header.Set(HeaderMeshSignature, sig)
	}
}

// computeSignature 生成 HMAC-SHA256(key, uid|agent_id|timestamp)。
func computeSignature(key []byte, uid, agentID, ts string) string {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(uid + "|" + agentID + "|" + ts))
	return hex.EncodeToString(mac.Sum(nil))
}

// VerifySignature 供 TrustGateway 调用，验证 Gateway 签名。
func VerifySignature(key []byte, uid, agentID, ts, sig string) bool {
	if len(key) == 0 {
		return true
	}
	expected := computeSignature(key, uid, agentID, ts)
	return hmac.Equal([]byte(expected), []byte(sig))
}

func isPublicPath(path string) bool {
	return strings.HasPrefix(path, "/v1/admin/auth/") ||
		path == "/v1/mesh/auth/token" ||
		path == "/healthz" ||
		path == "/readyz"
}

func requiredKind(path string) auth.TokenKind {
	if strings.HasPrefix(path, "/v1/mesh/") {
		return auth.KindAgent
	}
	if strings.HasPrefix(path, "/v1/admin/") {
		return auth.KindUser
	}
	return ""
}
