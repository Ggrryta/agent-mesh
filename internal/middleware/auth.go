package middleware

import (
	"context"
	"strings"

	"agent-gateway/config"
	"agent-gateway/internal/model"
	"agent-gateway/internal/repo"
	"agent-gateway/pkg/ctxkey"
	"agent-gateway/pkg/jwtclaims"
	"agent-gateway/pkg/resp"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
)

// ContextKeyAppID 保持向后兼容的别名
const ContextKeyAppID = ctxkey.AppID

// Auth 统一鉴权中间件，支持 JWT 和 API Key 双模式
// token 来源优先级：Authorization header > ?token= query param
// - Bearer token 以 agw_ 开头 → API Key 路径
// - 其余 → JWT 路径
func Auth(cfg *config.JWTConfig, apiKeyRepo *repo.APIKeyRepo) app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		tokenStr := extractToken(c)
		if tokenStr == "" {
			c.JSON(consts.StatusUnauthorized, resp.Err(resp.CodeUnauthorized, "missing token: provide Authorization header or ?token= query param"))
			c.Abort()
			return
		}

		if strings.HasPrefix(tokenStr, model.APIKeyPrefix) {
			authWithAPIKey(ctx, c, tokenStr, apiKeyRepo)
			return
		}
		authWithJWT(ctx, c, tokenStr, cfg)
	}
}

// JWTAuth 保持向后兼容，仅 JWT 模式（无 API Key repo 时使用）
func JWTAuth(cfg *config.JWTConfig) app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		authHeader := string(c.GetHeader("Authorization"))
		if authHeader == "" {
			c.JSON(consts.StatusUnauthorized, resp.Err(resp.CodeUnauthorized, "missing Authorization header"))
			c.Abort()
			return
		}
		parts := strings.SplitN(authHeader, " ", 2)
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
			c.JSON(consts.StatusUnauthorized, resp.Err(resp.CodeUnauthorized, "invalid Authorization format, expected: Bearer <token>"))
			c.Abort()
			return
		}
		authWithJWT(ctx, c, parts[1], cfg)
	}
}

func authWithAPIKey(ctx context.Context, c *app.RequestContext, keyPlain string, apiKeyRepo *repo.APIKeyRepo) {
	if len(keyPlain) < 8 {
		c.JSON(consts.StatusUnauthorized, resp.Err(resp.CodeUnauthorized, "invalid api key"))
		c.Abort()
		return
	}
	prefix := keyPlain[:8]

	record, err := apiKeyRepo.GetByPrefix(ctx, prefix)
	if err != nil || record == nil {
		c.JSON(consts.StatusUnauthorized, resp.Err(resp.CodeUnauthorized, "invalid api key"))
		c.Abort()
		return
	}

	if err := bcrypt.CompareHashAndPassword([]byte(record.KeyHash), []byte(keyPlain)); err != nil {
		c.JSON(consts.StatusUnauthorized, resp.Err(resp.CodeUnauthorized, "invalid api key"))
		c.Abort()
		return
	}

	c.Set(ctxkey.AppID, record.AppID)
	// 异步更新最后使用时间，不阻塞请求
	go apiKeyRepo.UpdateLastUsed(context.Background(), record.AppID)
	c.Next(ctx)
}

func authWithJWT(ctx context.Context, c *app.RequestContext, tokenStr string, cfg *config.JWTConfig) {
	claims := &jwtclaims.Claims{}
	token, err := jwt.ParseWithClaims(tokenStr, claims, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, jwt.ErrSignatureInvalid
		}
		return []byte(cfg.Secret), nil
	})
	if err != nil || !token.Valid {
		c.JSON(consts.StatusUnauthorized, resp.Err(resp.CodeUnauthorized, "invalid or expired token"))
		c.Abort()
		return
	}

	c.Set(ctxkey.AppID, claims.AppID)
	c.Next(ctx)
}

// extractToken 从 Authorization header 或 ?token= query param 中提取 token
func extractToken(c *app.RequestContext) string {
	authHeader := string(c.GetHeader("Authorization"))
	if authHeader != "" {
		parts := strings.SplitN(authHeader, " ", 2)
		if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
			return parts[1]
		}
		return ""
	}
	return string(c.Query("token"))
}

// InternalAuth 内部接口鉴权中间件，校验 X-Internal-Token header。
// token 为空字符串时跳过校验（仅限开发/测试环境）。
func InternalAuth(token string) app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		if token == "" {
			c.Next(ctx)
			return
		}
		got := string(c.GetHeader("X-Internal-Token"))
		if got != token {
			c.JSON(consts.StatusUnauthorized, resp.Err(resp.CodeUnauthorized, "invalid internal token"))
			c.Abort()
			return
		}
		c.Next(ctx)
	}
}
