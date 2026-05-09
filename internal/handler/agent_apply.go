package handler

import (
	"context"
	"errors"
	"strconv"

	"agent-gateway/internal/model"
	"agent-gateway/internal/repo"
	"agent-gateway/pkg/ctxkey"
	"agent-gateway/pkg/resp"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
)

// AgentApplyHandler 处理 Agent 调用权限申请/审批
type AgentApplyHandler struct {
	applyRepo *repo.AgentApplyRepo
	permRepo  *repo.AgentPermissionRepo
	agentRepo *repo.AgentRepo
}

func NewAgentApplyHandler(applyRepo *repo.AgentApplyRepo, permRepo *repo.AgentPermissionRepo, agentRepo *repo.AgentRepo) *AgentApplyHandler {
	return &AgentApplyHandler{applyRepo: applyRepo, permRepo: permRepo, agentRepo: agentRepo}
}

// Apply POST /agents/:agent_id/apply
func (h *AgentApplyHandler) Apply(ctx context.Context, c *app.RequestContext) {
	callerAppID := c.GetString(ctxkey.AppID)
	if callerAppID == "" {
		c.JSON(consts.StatusUnauthorized, resp.Err(resp.CodeUnauthorized, "unauthorized"))
		return
	}

	agentID := c.Param("agent_id")
	agent, err := h.agentRepo.GetByAgentID(ctx, agentID)
	if errors.Is(err, repo.ErrAgentNotFound) {
		c.JSON(consts.StatusNotFound, resp.Err(resp.CodeNotFound, "agent not found"))
		return
	}
	if err != nil {
		c.JSON(consts.StatusInternalServerError, resp.Err(resp.CodeInternalServerError, err.Error()))
		return
	}

	var req struct {
		Reason string `json:"reason"`
	}
	if err := c.BindJSON(&req); err != nil && len(c.Request.Body()) > 0 {
		c.JSON(consts.StatusBadRequest, resp.Err(resp.CodeBadRequest, "invalid request body"))
		return
	}

	apply := &model.AgentApply{
		AgentID:        agentID,
		OwnerAppID:     agent.OwnerAppID,
		ApplicantAppID: callerAppID,
		Reason:         req.Reason,
		Status:         model.ApplyStatusPending,
	}
	if err := h.applyRepo.Create(ctx, apply); err != nil {
		if errors.Is(err, repo.ErrAgentApplyDuplicate) {
			c.JSON(consts.StatusBadRequest, resp.Err(resp.CodeBadRequest, "apply already pending"))
			return
		}
		c.JSON(consts.StatusInternalServerError, resp.Err(resp.CodeInternalServerError, err.Error()))
		return
	}
	c.JSON(consts.StatusOK, resp.OK(apply))
}

// Inbox GET /agents/apply/inbox — 查看收到的申请（Agent owner）
func (h *AgentApplyHandler) Inbox(ctx context.Context, c *app.RequestContext) {
	callerAppID := c.GetString(ctxkey.AppID)
	if callerAppID == "" {
		c.JSON(consts.StatusUnauthorized, resp.Err(resp.CodeUnauthorized, "unauthorized"))
		return
	}
	applies, err := h.applyRepo.ListInbox(ctx, callerAppID, nil)
	if err != nil {
		c.JSON(consts.StatusInternalServerError, resp.Err(resp.CodeInternalServerError, err.Error()))
		return
	}
	c.JSON(consts.StatusOK, resp.OK(applies))
}

// Outbox GET /agents/apply/outbox — 查看发出的申请（Consumer）
func (h *AgentApplyHandler) Outbox(ctx context.Context, c *app.RequestContext) {
	callerAppID := c.GetString(ctxkey.AppID)
	if callerAppID == "" {
		c.JSON(consts.StatusUnauthorized, resp.Err(resp.CodeUnauthorized, "unauthorized"))
		return
	}
	applies, err := h.applyRepo.ListOutbox(ctx, callerAppID, nil)
	if err != nil {
		c.JSON(consts.StatusInternalServerError, resp.Err(resp.CodeInternalServerError, err.Error()))
		return
	}
	c.JSON(consts.StatusOK, resp.OK(applies))
}

// Approve POST /agents/apply/:apply_id/approve
func (h *AgentApplyHandler) Approve(ctx context.Context, c *app.RequestContext) {
	callerAppID := c.GetString(ctxkey.AppID)
	if callerAppID == "" {
		c.JSON(consts.StatusUnauthorized, resp.Err(resp.CodeUnauthorized, "unauthorized"))
		return
	}

	applyID, err := strconv.ParseInt(c.Param("apply_id"), 10, 64)
	if err != nil {
		c.JSON(consts.StatusBadRequest, resp.Err(resp.CodeBadRequest, "invalid apply_id"))
		return
	}

	apply, err := h.applyRepo.GetByID(ctx, applyID)
	if errors.Is(err, repo.ErrAgentApplyNotFound) {
		c.JSON(consts.StatusNotFound, resp.Err(resp.CodeNotFound, "apply not found"))
		return
	}
	if err != nil {
		c.JSON(consts.StatusInternalServerError, resp.Err(resp.CodeInternalServerError, err.Error()))
		return
	}
	if apply.OwnerAppID != callerAppID {
		c.JSON(consts.StatusForbidden, resp.Err(resp.CodeForbidden, "not the agent owner"))
		return
	}

	if err := h.applyRepo.Approve(ctx, applyID, apply.AgentID, apply.OwnerAppID, apply.ApplicantAppID); err != nil {
		if errors.Is(err, repo.ErrAgentApplyNotFound) {
			c.JSON(consts.StatusBadRequest, resp.Err(resp.CodeBadRequest, "apply not in pending state"))
			return
		}
		c.JSON(consts.StatusInternalServerError, resp.Err(resp.CodeInternalServerError, err.Error()))
		return
	}
	c.JSON(consts.StatusOK, resp.OK(nil))
}

// Reject POST /agents/apply/:apply_id/reject
func (h *AgentApplyHandler) Reject(ctx context.Context, c *app.RequestContext) {
	callerAppID := c.GetString(ctxkey.AppID)
	if callerAppID == "" {
		c.JSON(consts.StatusUnauthorized, resp.Err(resp.CodeUnauthorized, "unauthorized"))
		return
	}

	applyID, err := strconv.ParseInt(c.Param("apply_id"), 10, 64)
	if err != nil {
		c.JSON(consts.StatusBadRequest, resp.Err(resp.CodeBadRequest, "invalid apply_id"))
		return
	}

	apply, err := h.applyRepo.GetByID(ctx, applyID)
	if errors.Is(err, repo.ErrAgentApplyNotFound) {
		c.JSON(consts.StatusNotFound, resp.Err(resp.CodeNotFound, "apply not found"))
		return
	}
	if err != nil {
		c.JSON(consts.StatusInternalServerError, resp.Err(resp.CodeInternalServerError, err.Error()))
		return
	}
	if apply.OwnerAppID != callerAppID {
		c.JSON(consts.StatusForbidden, resp.Err(resp.CodeForbidden, "not the agent owner"))
		return
	}

	if err := h.applyRepo.Reject(ctx, applyID); err != nil {
		if errors.Is(err, repo.ErrAgentApplyNotFound) {
			c.JSON(consts.StatusBadRequest, resp.Err(resp.CodeBadRequest, "apply not in pending state"))
			return
		}
		c.JSON(consts.StatusInternalServerError, resp.Err(resp.CodeInternalServerError, err.Error()))
		return
	}
	c.JSON(consts.StatusOK, resp.OK(nil))
}

// Revoke DELETE /agents/:agent_id/permissions/:consumer_app_id
func (h *AgentApplyHandler) Revoke(ctx context.Context, c *app.RequestContext) {
	callerAppID := c.GetString(ctxkey.AppID)
	if callerAppID == "" {
		c.JSON(consts.StatusUnauthorized, resp.Err(resp.CodeUnauthorized, "unauthorized"))
		return
	}

	agentID := c.Param("agent_id")
	consumerAppID := c.Param("consumer_app_id")

	agent, err := h.agentRepo.GetByAgentID(ctx, agentID)
	if errors.Is(err, repo.ErrAgentNotFound) {
		c.JSON(consts.StatusNotFound, resp.Err(resp.CodeNotFound, "agent not found"))
		return
	}
	if err != nil {
		c.JSON(consts.StatusInternalServerError, resp.Err(resp.CodeInternalServerError, err.Error()))
		return
	}
	if agent.OwnerAppID != callerAppID {
		c.JSON(consts.StatusForbidden, resp.Err(resp.CodeForbidden, "not the agent owner"))
		return
	}

	if err := h.permRepo.Revoke(ctx, agentID, consumerAppID); err != nil {
		if errors.Is(err, repo.ErrAgentPermissionNotFound) {
			c.JSON(consts.StatusNotFound, resp.Err(resp.CodeNotFound, "permission not found"))
			return
		}
		c.JSON(consts.StatusInternalServerError, resp.Err(resp.CodeInternalServerError, err.Error()))
		return
	}
	c.JSON(consts.StatusOK, resp.OK(nil))
}
