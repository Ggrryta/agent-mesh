package handler

import (
	"context"
	"strconv"

	"agent-gateway/internal/repo"
	"agent-gateway/internal/service"
	"agent-gateway/pkg/ctxkey"
	"agent-gateway/pkg/resp"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
)

// FriendshipHandler 管理 agent 间对称好友关系
// 所有接口都要求 ctxkey.AgentID 已被 AgentAuth 注入
type FriendshipHandler struct {
	repo *repo.FriendshipRepo
	hub  *service.InboxHub
}

func NewFriendshipHandler(repo *repo.FriendshipRepo, hub *service.InboxHub) *FriendshipHandler {
	return &FriendshipHandler{repo: repo, hub: hub}
}

// Request POST /friendships/request
// body: {target_agent_id, reason}
func (h *FriendshipHandler) Request(ctx context.Context, c *app.RequestContext) {
	self := c.GetString(ctxkey.AgentID)
	var req struct {
		TargetAgentID string `json:"target_agent_id"`
		Reason        string `json:"reason"`
	}
	if err := c.BindJSON(&req); err != nil {
		c.JSON(consts.StatusBadRequest, resp.Err(resp.CodeBadRequest, "invalid body"))
		return
	}
	req.TargetAgentID = service.NormalizeAgentID(req.TargetAgentID)
	if req.TargetAgentID == "" {
		c.JSON(consts.StatusBadRequest, resp.Err(resp.CodeBadRequest, "target_agent_id required"))
		return
	}
	f, err := h.repo.Request(ctx, self, req.TargetAgentID, req.Reason)
	if err != nil {
		switch err {
		case repo.ErrFriendshipSelf:
			c.JSON(consts.StatusBadRequest, resp.Err(resp.CodeBadRequest, "cannot friend yourself"))
		default:
			c.JSON(consts.StatusInternalServerError, resp.Err(resp.CodeInternalServerError, err.Error()))
		}
		return
	}

	// 通知被请求方(如果在线)
	if h.hub != nil {
		h.hub.Publish(req.TargetAgentID, service.InboxEventFriendReq, map[string]any{
			"friendship_id": f.ID,
			"initiator":     self,
			"reason":        req.Reason,
			"status":        f.Status,
		})
	}

	c.JSON(consts.StatusOK, resp.OK(f))
}

// Accept POST /friendships/:id/accept
func (h *FriendshipHandler) Accept(ctx context.Context, c *app.RequestContext) {
	self := c.GetString(ctxkey.AgentID)
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(consts.StatusBadRequest, resp.Err(resp.CodeBadRequest, "invalid id"))
		return
	}
	if err := h.repo.Accept(ctx, id, self); err != nil {
		if err == repo.ErrFriendshipNotFound {
			c.JSON(consts.StatusNotFound, resp.Err(resp.CodeNotFound, "friendship not found or not receivable"))
			return
		}
		c.JSON(consts.StatusInternalServerError, resp.Err(resp.CodeInternalServerError, err.Error()))
		return
	}
	// 通知请求方
	f, err := h.repo.GetByID(ctx, id)
	if err == nil && h.hub != nil {
		other := f.Counterpart(self)
		h.hub.Publish(other, service.InboxEventFriendAccept, map[string]any{
			"friendship_id": f.ID,
			"by":            self,
		})
	}
	c.JSON(consts.StatusOK, resp.OK(nil))
}

// Reject POST /friendships/:id/reject
func (h *FriendshipHandler) Reject(ctx context.Context, c *app.RequestContext) {
	self := c.GetString(ctxkey.AgentID)
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(consts.StatusBadRequest, resp.Err(resp.CodeBadRequest, "invalid id"))
		return
	}
	if err := h.repo.Reject(ctx, id, self); err != nil {
		if err == repo.ErrFriendshipNotFound {
			c.JSON(consts.StatusNotFound, resp.Err(resp.CodeNotFound, "friendship not found"))
			return
		}
		c.JSON(consts.StatusInternalServerError, resp.Err(resp.CodeInternalServerError, err.Error()))
		return
	}
	c.JSON(consts.StatusOK, resp.OK(nil))
}

// Revoke POST /friendships/:id/revoke
// 任一方都能解除已建立的好友
func (h *FriendshipHandler) Revoke(ctx context.Context, c *app.RequestContext) {
	self := c.GetString(ctxkey.AgentID)
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(consts.StatusBadRequest, resp.Err(resp.CodeBadRequest, "invalid id"))
		return
	}
	f, _ := h.repo.GetByID(ctx, id)
	if err := h.repo.Revoke(ctx, id, self); err != nil {
		if err == repo.ErrFriendshipNotFound {
			c.JSON(consts.StatusNotFound, resp.Err(resp.CodeNotFound, "friendship not found"))
			return
		}
		c.JSON(consts.StatusInternalServerError, resp.Err(resp.CodeInternalServerError, err.Error()))
		return
	}
	// 通知对方
	if f != nil && h.hub != nil {
		other := f.Counterpart(self)
		h.hub.Publish(other, service.InboxEventFriendRevoke, map[string]any{
			"friendship_id": f.ID,
			"by":            self,
		})
	}
	c.JSON(consts.StatusOK, resp.OK(nil))
}

// ListFriends GET /friendships
func (h *FriendshipHandler) ListFriends(ctx context.Context, c *app.RequestContext) {
	self := c.GetString(ctxkey.AgentID)
	list, err := h.repo.ListFriends(ctx, self)
	if err != nil {
		c.JSON(consts.StatusInternalServerError, resp.Err(resp.CodeInternalServerError, err.Error()))
		return
	}
	out := make([]map[string]any, 0, len(list))
	for _, f := range list {
		out = append(out, map[string]any{
			"id":          f.ID,
			"friend":      f.Counterpart(self),
			"accepted_at": f.AcceptedAt,
		})
	}
	c.JSON(consts.StatusOK, resp.OK(out))
}

// ListPending GET /friendships/pending
func (h *FriendshipHandler) ListPending(ctx context.Context, c *app.RequestContext) {
	self := c.GetString(ctxkey.AgentID)
	list, err := h.repo.ListPending(ctx, self)
	if err != nil {
		c.JSON(consts.StatusInternalServerError, resp.Err(resp.CodeInternalServerError, err.Error()))
		return
	}
	out := make([]map[string]any, 0, len(list))
	for _, f := range list {
		out = append(out, map[string]any{
			"id":         f.ID,
			"initiator":  f.InitiatorID,
			"reason":     f.Reason,
			"created_at": f.CreatedAt,
		})
	}
	c.JSON(consts.StatusOK, resp.OK(out))
}
