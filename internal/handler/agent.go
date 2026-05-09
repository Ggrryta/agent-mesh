package handler

import (
	"context"
	"errors"

	"agent-gateway/internal/model"
	"agent-gateway/internal/repo"
	"agent-gateway/internal/service"
	"agent-gateway/pkg/ctxkey"
	"agent-gateway/pkg/resp"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
)

// AgentHandler 处理 Agent 注册/发现相关请求
type AgentHandler struct {
	svc      agentService
	permRepo agentPermissionChecker
}

type agentService interface {
	Register(ctx context.Context, ownerAppID string, req service.RegisterAgentRequest) (*model.Agent, error)
	Deregister(ctx context.Context, agentID, callerAppID string) error
	Get(ctx context.Context, agentID string) (*model.Agent, error)
	Drain(ctx context.Context, agentID, callerAppID string) error
	List(ctx context.Context, f repo.AgentFilter) ([]*model.Agent, int64, error)
	GetSkills(ctx context.Context, agentID string) ([]*model.AgentSkill, error)
}

type agentPermissionChecker interface {
	HasPermission(ctx context.Context, agentID, consumerAppID string) (bool, error)
}

func NewAgentHandler(svc *service.AgentService, permRepo *repo.AgentPermissionRepo) *AgentHandler {
	return &AgentHandler{svc: svc, permRepo: permRepo}
}

// Register POST /agents/register
func (h *AgentHandler) Register(ctx context.Context, c *app.RequestContext) {
	callerAppID := c.GetString(ctxkey.AppID)
	if callerAppID == "" {
		c.JSON(consts.StatusUnauthorized, resp.Err(resp.CodeUnauthorized, "unauthorized"))
		return
	}

	var req service.RegisterAgentRequest
	if err := c.BindJSON(&req); err != nil {
		c.JSON(consts.StatusBadRequest, resp.Err(resp.CodeBadRequest, "invalid request body: "+err.Error()))
		return
	}

	agent, err := h.svc.Register(ctx, callerAppID, req)
	if err != nil {
		if errors.Is(err, service.ErrForbiddenNotOwner) {
			c.JSON(consts.StatusForbidden, resp.Err(resp.CodeForbidden, err.Error()))
			return
		}
		c.JSON(consts.StatusBadRequest, resp.Err(resp.CodeBadRequest, err.Error()))
		return
	}
	c.JSON(consts.StatusOK, resp.OK(agent))
}

// Deregister DELETE /agents/:agent_id
func (h *AgentHandler) Deregister(ctx context.Context, c *app.RequestContext) {
	callerAppID := c.GetString(ctxkey.AppID)
	if callerAppID == "" {
		c.JSON(consts.StatusUnauthorized, resp.Err(resp.CodeUnauthorized, "unauthorized"))
		return
	}

	agentID := c.Param("agent_id")
	err := h.svc.Deregister(ctx, agentID, callerAppID)
	if errors.Is(err, repo.ErrAgentNotFound) {
		c.JSON(consts.StatusNotFound, resp.Err(resp.CodeNotFound, "agent not found"))
		return
	}
	if err != nil {
		if errors.Is(err, service.ErrForbiddenNotOwner) {
			c.JSON(consts.StatusForbidden, resp.Err(resp.CodeForbidden, err.Error()))
			return
		}
		c.JSON(consts.StatusInternalServerError, resp.Err(resp.CodeInternalServerError, err.Error()))
		return
	}
	c.JSON(consts.StatusOK, resp.OK(nil))
}

// Get GET /agents/:agent_id
func (h *AgentHandler) Get(ctx context.Context, c *app.RequestContext) {
	agentID := c.Param("agent_id")
	agent, ok := h.getVisibleAgent(ctx, c, agentID)
	if !ok {
		return
	}
	c.JSON(consts.StatusOK, resp.OK(agent))
}

// Drain PUT /agents/:agent_id/drain — 优雅下线，停止路由新请求
func (h *AgentHandler) Drain(ctx context.Context, c *app.RequestContext) {
	callerAppID := c.GetString(ctxkey.AppID)
	if callerAppID == "" {
		c.JSON(consts.StatusUnauthorized, resp.Err(resp.CodeUnauthorized, "unauthorized"))
		return
	}

	agentID := c.Param("agent_id")
	err := h.svc.Drain(ctx, agentID, callerAppID)
	if errors.Is(err, repo.ErrAgentNotFound) {
		c.JSON(consts.StatusNotFound, resp.Err(resp.CodeNotFound, "agent not found"))
		return
	}
	if err != nil {
		if errors.Is(err, service.ErrForbiddenNotOwner) {
			c.JSON(consts.StatusForbidden, resp.Err(resp.CodeForbidden, err.Error()))
			return
		}
		c.JSON(consts.StatusInternalServerError, resp.Err(resp.CodeInternalServerError, err.Error()))
		return
	}
	c.JSON(consts.StatusOK, resp.OK(nil))
}

// GetCard GET /agents/:agent_id/card
func (h *AgentHandler) GetCard(ctx context.Context, c *app.RequestContext) {
	agentID := c.Param("agent_id")
	agent, ok := h.getVisibleAgent(ctx, c, agentID)
	if !ok {
		return
	}
	// 直接返回原始 AgentCard JSON
	c.Data(consts.StatusOK, "application/json", agent.AgentCardJSON)
}

// List GET /agents
func (h *AgentHandler) List(ctx context.Context, c *app.RequestContext) {
	callerAppID := c.GetString(ctxkey.AppID)
	page := parsePageParam(c.Query("page"), 1)
	pageSize := parsePageParam(c.Query("page_size"), 20)
	f := repo.AgentFilter{
		Keyword:  c.Query("keyword"),
		Tag:      c.Query("tag"),
		Page:     page,
		PageSize: pageSize,
	}

	// 可选：只看自己注册的
	if c.Query("mine") == "true" {
		if callerAppID == "" {
			c.JSON(consts.StatusUnauthorized, resp.Err(resp.CodeUnauthorized, "unauthorized"))
			return
		}
		f.OwnerAppID = callerAppID
	}

	agents, total, err := h.svc.List(ctx, f)
	if err != nil {
		c.JSON(consts.StatusInternalServerError, resp.Err(resp.CodeInternalServerError, err.Error()))
		return
	}

	if f.OwnerAppID == "" {
		agents, err = h.filterVisibleAgents(ctx, agents, callerAppID)
		if err != nil {
			c.JSON(consts.StatusInternalServerError, resp.Err(resp.CodeInternalServerError, err.Error()))
			return
		}
		total = int64(len(agents))
		agents = paginateAgents(agents, page, pageSize)
	}
	c.JSON(consts.StatusOK, resp.OK(map[string]any{
		"total": total,
		"list":  agents,
	}))
}

// GetSkills GET /agents/:agent_id/skills
func (h *AgentHandler) GetSkills(ctx context.Context, c *app.RequestContext) {
	agentID := c.Param("agent_id")
	if _, ok := h.getVisibleAgent(ctx, c, agentID); !ok {
		return
	}
	skills, err := h.svc.GetSkills(ctx, agentID)
	if err != nil {
		c.JSON(consts.StatusInternalServerError, resp.Err(resp.CodeInternalServerError, err.Error()))
		return
	}
	c.JSON(consts.StatusOK, resp.OK(skills))
}

func (h *AgentHandler) getVisibleAgent(ctx context.Context, c *app.RequestContext, agentID string) (*model.Agent, bool) {
	agent, err := h.svc.Get(ctx, agentID)
	if errors.Is(err, repo.ErrAgentNotFound) {
		c.JSON(consts.StatusNotFound, resp.Err(resp.CodeNotFound, "agent not found"))
		return nil, false
	}
	if err != nil {
		c.JSON(consts.StatusInternalServerError, resp.Err(resp.CodeInternalServerError, err.Error()))
		return nil, false
	}

	allowed, err := h.canReadAgent(ctx, agent, c.GetString(ctxkey.AppID))
	if err != nil {
		c.JSON(consts.StatusInternalServerError, resp.Err(resp.CodeInternalServerError, err.Error()))
		return nil, false
	}
	if !allowed {
		c.JSON(consts.StatusNotFound, resp.Err(resp.CodeNotFound, "agent not found"))
		return nil, false
	}
	return agent, true
}

func (h *AgentHandler) filterVisibleAgents(ctx context.Context, agents []*model.Agent, callerAppID string) ([]*model.Agent, error) {
	visible := make([]*model.Agent, 0, len(agents))
	for _, agent := range agents {
		allowed, err := h.canReadAgent(ctx, agent, callerAppID)
		if err != nil {
			return nil, err
		}
		if allowed {
			visible = append(visible, agent)
		}
	}
	return visible, nil
}

func (h *AgentHandler) canReadAgent(ctx context.Context, agent *model.Agent, callerAppID string) (bool, error) {
	if agent == nil {
		return false, nil
	}
	if agent.Visibility != model.VisibilityPrivate {
		return true, nil
	}
	if callerAppID == "" {
		return false, nil
	}
	if callerAppID == agent.OwnerAppID {
		return true, nil
	}
	if h.permRepo == nil {
		return false, nil
	}
	return h.permRepo.HasPermission(ctx, agent.AgentID, callerAppID)
}

func paginateAgents(agents []*model.Agent, page, pageSize int) []*model.Agent {
	if page <= 0 || pageSize <= 0 {
		return agents
	}
	start := (page - 1) * pageSize
	if start >= len(agents) {
		return []*model.Agent{}
	}
	end := start + pageSize
	if end > len(agents) {
		end = len(agents)
	}
	return agents[start:end]
}

func parsePageParam(s string, defaultVal int) int {
	if s == "" {
		return defaultVal
	}
	n := 0
	for _, ch := range s {
		if ch < '0' || ch > '9' {
			return defaultVal
		}
		n = n*10 + int(ch-'0')
	}
	if n <= 0 {
		return defaultVal
	}
	return n
}
