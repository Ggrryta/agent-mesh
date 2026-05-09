package middleware

import (
	"context"
	"strings"

	"agent-gateway/pkg/ctxkey"
	"agent-gateway/pkg/resp"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
)

// AdminAuth Admin API 鉴权中间件。
// adminToken 为空时直接拒绝，避免在未配置时裸奔暴露管理接口。
func AdminAuth(adminToken string) app.HandlerFunc {
	return func(c context.Context, ctx *app.RequestContext) {
		adminToken = strings.TrimSpace(adminToken)
		if adminToken == "" {
			ctx.AbortWithStatusJSON(consts.StatusServiceUnavailable, resp.Err(resp.CodeServiceUnavailable, "admin api is disabled: admin token not configured"))
			return
		}

		token := string(ctx.GetHeader("X-Admin-Token"))
		if token == "" {
			token = ctx.Query("admin_token")
		}

		if token == "" || token != adminToken {
			ctx.AbortWithStatusJSON(consts.StatusUnauthorized, resp.Err(resp.CodeUnauthorized, "admin token required"))
			return
		}

		ctx.Set(ctxkey.Admin, true)
		ctx.Next(c)
	}
}

// RequireAdminToken 检查管理接口是否已配置 token。
func RequireAdminToken(adminToken string) bool {
	return strings.TrimSpace(adminToken) != ""
}
