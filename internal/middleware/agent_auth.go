package middleware

import (
	"context"
	"strings"

	"agent-gateway/internal/repo"
	"agent-gateway/pkg/ctxkey"
	"agent-gateway/pkg/resp"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
)

// AgentAuth 强制携带 X-Agent-ID header 或路径参数 :agent_id
// 校验该 agent_id 确属当前 app_id,校验通过后注入 ctxkey.AgentID
// 所有 agent_id 在此统一规范化(ToLower + TrimSpace),避免下游大小写不一致
// 必须在 Auth 之后使用
func AgentAuth(agentRepo *repo.AgentRepo) app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		appID := c.GetString(ctxkey.AppID)
		if appID == "" {
			c.JSON(consts.StatusUnauthorized, resp.Err(resp.CodeUnauthorized, "missing app_id"))
			c.Abort()
			return
		}

		agentID := strings.ToLower(strings.TrimSpace(string(c.GetHeader("X-Agent-ID"))))
		if agentID == "" {
			agentID = strings.ToLower(strings.TrimSpace(c.Param("agent_id")))
		}
		if agentID == "" {
			c.JSON(consts.StatusBadRequest, resp.Err(resp.CodeBadRequest,
				"missing agent identity: provide X-Agent-ID header or :agent_id in path"))
			c.Abort()
			return
		}

		agent, err := agentRepo.GetByAgentID(ctx, agentID)
		if err != nil {
			if err == repo.ErrAgentNotFound {
				c.JSON(consts.StatusNotFound, resp.Err(resp.CodeNotFound, "agent not found: "+agentID))
				c.Abort()
				return
			}
			c.JSON(consts.StatusInternalServerError, resp.Err(resp.CodeInternalServerError, err.Error()))
			c.Abort()
			return
		}
		if agent.OwnerAppID != appID {
			c.JSON(consts.StatusForbidden, resp.Err(resp.CodeForbidden,
				"agent "+agentID+" is not owned by "+appID))
			c.Abort()
			return
		}

		c.Set(ctxkey.AgentID, agentID)
		c.Next(ctx)
	}
}
