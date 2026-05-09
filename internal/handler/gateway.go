package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"time"

	"agent-gateway/internal/model"
	"agent-gateway/internal/repo"
	"agent-gateway/internal/service"
	"agent-gateway/pkg/circuitbreaker"
	"agent-gateway/pkg/ctxkey"
	"agent-gateway/pkg/resp"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
	"github.com/google/uuid"
)

const (
	asyncTaskRedisPrefix = "async:task:"
	asyncTaskRedisTTL    = 48 * time.Hour
)

// GatewayHandler 以 Agent 为核心的网关，通过 A2A JSON-RPC 调用下游 Agent
type GatewayHandler struct {
	agentCache       *service.AgentCache
	agentSkillRepo   agentSkillRepo
	agentPermRepo    *repo.AgentPermissionRepo
	a2aInvoker       a2aInvoker
	callGuard        *service.AgentCallGuard
	taskRepo         taskRepo
	reliableTaskRepo taskRepo
}

type taskRepo interface {
	Create(ctx context.Context, t *model.AsyncTask) error
	GetByTaskID(ctx context.Context, taskID string) (*model.AsyncTask, error)
}

type agentSkillRepo interface {
	GetByAgentIDAndSkillID(ctx context.Context, agentID, skillID string) (*model.AgentSkill, error)
}

type a2aInvoker interface {
	Send(ctx context.Context, agentURL string, input json.RawMessage, skillID string) (json.RawMessage, error)
	Stream(ctx context.Context, agentURL string, input json.RawMessage, skillID string, w io.Writer) error
}

func NewGatewayHandler(
	agentCache *service.AgentCache,
	agentSkillRepo *repo.AgentSkillRepo,
	agentPermRepo *repo.AgentPermissionRepo,
	a2aInvoker *service.A2AInvoker,
	callGuard *service.AgentCallGuard,
	asyncTaskRepo *repo.AsyncTaskRepo,
	reliableTaskRepos ...taskRepo,
) *GatewayHandler {
	var reliableTaskRepo taskRepo
	if len(reliableTaskRepos) > 0 {
		reliableTaskRepo = reliableTaskRepos[0]
	}
	return &GatewayHandler{
		agentCache:       agentCache,
		agentSkillRepo:   agentSkillRepo,
		agentPermRepo:    agentPermRepo,
		a2aInvoker:       a2aInvoker,
		callGuard:        callGuard,
		taskRepo:         asyncTaskRepo,
		reliableTaskRepo: reliableTaskRepo,
	}
}

// InvokeAgent POST /gateway/invoke/agent/:agent_id[?async=true][?stream=true]
// 公开 agent 无需 token；私有 agent 需要 token + 权限名单（owner 自身免检）
func (h *GatewayHandler) InvokeAgent(ctx context.Context, c *app.RequestContext) {
	agentID := c.Param("agent_id")
	skillID := c.Param("skill_id") // 可选，来自 /gateway/invoke/agent/:agent_id/skill/:skill_id

	agent, ok := h.agentCache.Get(agentID)
	if !ok {
		c.JSON(consts.StatusNotFound, resp.Err(resp.CodeNotFound, "agent not found: "+agentID))
		return
	}

	if agent.Status != model.AgentStatusActive {
		c.JSON(consts.StatusServiceUnavailable, resp.Err(resp.CodeServiceUnavailable, "agent is not active: "+agentID))
		return
	}
	if skillID != "" {
		if h.agentSkillRepo == nil {
			c.JSON(consts.StatusInternalServerError, resp.Err(resp.CodeInternalServerError, "agent skill repo is not configured"))
			return
		}
		if _, err := h.agentSkillRepo.GetByAgentIDAndSkillID(ctx, agentID, skillID); err != nil {
			if errors.Is(err, repo.ErrAgentSkillNotFound) {
				c.JSON(consts.StatusNotFound, resp.Err(resp.CodeNotFound, "skill not found for agent: "+agentID+"/"+skillID))
				return
			}
			c.JSON(consts.StatusInternalServerError, resp.Err(resp.CodeInternalServerError, "query agent skill failed"))
			return
		}
	}

	// 权限检查：私有 agent 需要 token + 名单（owner 自身免检）
	if agent.Visibility == model.VisibilityPrivate {
		callerAppID := c.GetString(ctxkey.AppID)
		if callerAppID != agent.OwnerAppID {
			has, err := h.agentPermRepo.HasPermission(ctx, agentID, callerAppID)
			if err != nil {
				c.JSON(consts.StatusInternalServerError, resp.Err(resp.CodeInternalServerError, "check permission failed"))
				return
			}
			if !has {
				c.JSON(consts.StatusForbidden, resp.Err(resp.CodeForbidden, "no permission to invoke agent: "+agentID+", please apply first"))
				return
			}
		}
	}

	var rawInput json.RawMessage
	if err := c.BindJSON(&rawInput); err != nil {
		c.JSON(consts.StatusBadRequest, resp.Err(resp.CodeBadRequest, "invalid request body: "+err.Error()))
		return
	}

	if string(c.Query("async")) == "true" {
		h.invokeAsync(ctx, c, agent, skillID, rawInput)
		return
	}

	if string(c.Query("stream")) == "true" {
		h.invokeStream(ctx, c, agent, skillID, rawInput)
		return
	}

	output, err := h.callGuard.Execute(agentID, func() (json.RawMessage, error) {
		return h.a2aInvoker.Send(ctx, agent.URL, rawInput, skillID)
	})
	if err != nil {
		if errors.Is(err, circuitbreaker.ErrOpenState) {
			c.JSON(consts.StatusServiceUnavailable, resp.Err(resp.CodeServiceUnavailable, "circuit breaker open: "+agentID))
			return
		}
		c.JSON(consts.StatusBadGateway, resp.Err(resp.CodeBadGateway, "invoke agent failed: "+err.Error()))
		return
	}

	var result any
	_ = json.Unmarshal(output, &result)
	respData := map[string]any{
		"agent_id": agentID,
		"output":   result,
	}
	if skillID != "" {
		respData["skill_id"] = skillID
	}
	c.JSON(consts.StatusOK, resp.OK(respData))
}

func (h *GatewayHandler) invokeAsync(ctx context.Context, c *app.RequestContext, agent *model.Agent, skillID string, input json.RawMessage) {
	taskID := uuid.New().String()
	appID := c.GetString(ctxkey.AppID)

	task := &model.AsyncTask{
		TaskID:    taskID,
		AgentID:   agent.AgentID,
		SkillID:   skillID,
		AppID:     appID,
		Input:     input,
		Status:    model.AsyncTaskStatusPending,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	if c.Query("reliability") == model.ReliabilityReliable && h.reliableTaskRepo != nil {
		if err := h.reliableTaskRepo.Create(ctx, task); err != nil {
			c.JSON(consts.StatusInternalServerError, resp.Err(resp.CodeInternalServerError, "create reliable task failed: "+err.Error()))
			return
		}
		c.JSON(consts.StatusAccepted, resp.OK(map[string]any{"task_id": taskID, "reliability": model.ReliabilityReliable}))
		return
	}

	if err := h.taskRepo.Create(ctx, task); err != nil {
		c.JSON(consts.StatusInternalServerError, resp.Err(resp.CodeInternalServerError, "create task failed: "+err.Error()))
		return
	}

	c.JSON(consts.StatusAccepted, resp.OK(map[string]any{"task_id": taskID, "reliability": model.ReliabilityRedis}))
}

func (h *GatewayHandler) invokeStream(ctx context.Context, c *app.RequestContext, agent *model.Agent, skillID string, input json.RawMessage) {
	c.Response.Header.Set("Content-Type", "text/event-stream")
	c.Response.Header.Set("Cache-Control", "no-cache")
	c.Response.Header.Set("X-Accel-Buffering", "no")

	pr, pw := io.Pipe()
	c.SetBodyStream(pr, -1)

	go func() {
		defer pw.Close()
		if err := h.a2aInvoker.Stream(ctx, agent.URL, input, skillID, pw); err != nil {
			pw.Write([]byte("data: {\"error\":\"" + err.Error() + "\"}\n\n"))
		}
	}()
}

// GetTask GET /gateway/task/:task_id
func (h *GatewayHandler) GetTask(ctx context.Context, c *app.RequestContext) {
	taskID := c.Param("task_id")

	task, err := h.taskRepo.GetByTaskID(ctx, taskID)
	if err != nil {
		if errors.Is(err, repo.ErrTaskNotFound) && h.reliableTaskRepo != nil {
			task, err = h.reliableTaskRepo.GetByTaskID(ctx, taskID)
		}
	}
	if err != nil {
		if errors.Is(err, repo.ErrTaskNotFound) {
			c.JSON(consts.StatusNotFound, resp.Err(resp.CodeNotFound, "task not found: "+taskID))
		} else {
			c.JSON(consts.StatusInternalServerError, resp.Err(resp.CodeInternalServerError, "get task failed: "+err.Error()))
		}
		return
	}
	callerAppID := c.GetString(ctxkey.AppID)
	if task.AppID == "" || task.AppID != callerAppID {
		c.JSON(consts.StatusForbidden, resp.Err(resp.CodeForbidden, "no permission to access task: "+taskID))
		return
	}

	result := map[string]any{
		"task_id":    task.TaskID,
		"agent_id":   task.AgentID,
		"skill_id":   task.SkillID,
		"status":     task.Status,
		"created_at": task.CreatedAt,
		"updated_at": task.UpdatedAt,
	}
	if task.Status == model.AsyncTaskStatusCompleted && len(task.Output) > 0 {
		var output any
		_ = json.Unmarshal(task.Output, &output)
		result["output"] = output
	}
	if task.Status == model.AsyncTaskStatusFailed {
		result["error"] = task.ErrorMsg
	}

	c.JSON(consts.StatusOK, resp.OK(result))
}

// OnCircuitBreakerConfigChange 配置热更新回调：清空 breaker 缓存，下次请求用新配置重建
func (h *GatewayHandler) OnCircuitBreakerConfigChange() {
	if h.callGuard != nil {
		h.callGuard.Reset()
	}
}
