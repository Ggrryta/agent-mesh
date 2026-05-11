package handler

import (
	"context"
	"errors"
	"strconv"

	"agent-gateway/internal/model"
	"agent-gateway/internal/repo"
	"agent-gateway/internal/service"
	"agent-gateway/pkg/ctxkey"
	"agent-gateway/pkg/resp"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
)

// TaskV2Handler GAS 专用 task/消息 API
// 所有端点都要求 AgentAuth 注入 ctxkey.AgentID
type TaskV2Handler struct {
	dispatcher *service.AgentDispatcher
	repo       *repo.TaskV2Repo
}

func NewTaskV2Handler(dispatcher *service.AgentDispatcher, repo *repo.TaskV2Repo) *TaskV2Handler {
	return &TaskV2Handler{dispatcher: dispatcher, repo: repo}
}

// SendMessageReq send message 请求体
// target_agent_id 和 task_id 二选一:
//   - 有 task_id → 在现有 task 追加消息
//   - 只有 target_agent_id → 创建新 task
type SendMessageReq struct {
	TargetAgentID string                    `json:"target_agent_id,omitempty"`
	TaskID        string                    `json:"task_id,omitempty"`
	Title         string                    `json:"title,omitempty"`
	MessageID     string                    `json:"message_id"`
	Parts         []service.A2AMessagePart  `json:"parts"`
}

// SendMessage POST /v2/messages
// 同时支持新建 task 和在已有 task 追加
func (h *TaskV2Handler) SendMessage(ctx context.Context, c *app.RequestContext) {
	self := c.GetString(ctxkey.AgentID)
	var req SendMessageReq
	if err := c.BindJSON(&req); err != nil {
		c.JSON(consts.StatusBadRequest, resp.Err(resp.CodeBadRequest, "invalid body"))
		return
	}
	if len(req.Parts) == 0 {
		c.JSON(consts.StatusBadRequest, resp.Err(resp.CodeBadRequest, "parts required"))
		return
	}
	if req.MessageID == "" {
		c.JSON(consts.StatusBadRequest, resp.Err(resp.CodeBadRequest, "message_id required"))
		return
	}

	var input service.SendMessageInput
	input.Sender = self
	input.MessageID = req.MessageID
	input.Parts = req.Parts
	input.CallerAppID = c.GetString(ctxkey.AppID) // 用于 per-account 限流

	if req.TaskID != "" {
		// 在已有 task 追加: target 暂不需要,走 task member 校验
		// 但为了复用 dispatcher.SendMessage,target 需要从 task_members 推断
		members, err := h.repo.ListMembers(ctx, req.TaskID)
		if err != nil {
			c.JSON(consts.StatusInternalServerError, resp.Err(resp.CodeInternalServerError, err.Error()))
			return
		}
		if len(members) == 0 {
			c.JSON(consts.StatusNotFound, resp.Err(resp.CodeNotFound, "task not found: "+req.TaskID))
			return
		}
		// 找 task 中除 self 外的 agent(本期两方 task)
		found := false
		for _, m := range members {
			if m.AgentID == self {
				found = true
				continue
			}
			input.Target = m.AgentID
		}
		if !found {
			c.JSON(consts.StatusForbidden, resp.Err(resp.CodeTaskNotMember, "not a task member"))
			return
		}
		input.TaskID = req.TaskID
	} else {
		if req.TargetAgentID == "" {
			c.JSON(consts.StatusBadRequest, resp.Err(resp.CodeBadRequest, "target_agent_id or task_id required"))
			return
		}
		input.Target = service.NormalizeAgentID(req.TargetAgentID)
		input.Title = req.Title
	}

	result, err := h.dispatcher.SendMessage(ctx, input)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrNotFriend):
			c.JSON(consts.StatusForbidden, resp.Err(resp.CodeNotFriend, "not friend with target"))
		case errors.Is(err, service.ErrAgentOffline):
			c.JSON(consts.StatusServiceUnavailable, resp.Err(resp.CodeAgentOffline, "target agent offline"))
		case errors.Is(err, service.ErrAgentNotFound):
			c.JSON(consts.StatusNotFound, resp.Err(resp.CodeNotFound, "target agent not found"))
		case errors.Is(err, service.ErrRateLimited):
			c.JSON(consts.StatusTooManyRequests, resp.Err(resp.CodeRateLimited, "rate limit exceeded; slow down"))
		case errors.Is(err, repo.ErrTaskV2NotFound):
			c.JSON(consts.StatusNotFound, resp.Err(resp.CodeNotFound, "task not found or not a member"))
		case errors.Is(err, repo.ErrTaskV2BadState):
			c.JSON(consts.StatusConflict, resp.Err(resp.CodeTaskClosed, "task is closed"))
		default:
			c.JSON(consts.StatusInternalServerError, resp.Err(resp.CodeInternalServerError, err.Error()))
		}
		return
	}
	c.JSON(consts.StatusOK, resp.OK(result))
}

// GetTask GET /v2/tasks/:task_id
// 返回 task 元信息 + 成员 + 消息历史(带 limit/since_seq)
func (h *TaskV2Handler) GetTask(ctx context.Context, c *app.RequestContext) {
	self := c.GetString(ctxkey.AgentID)
	taskID := c.Param("task_id")

	isMember, err := h.repo.IsMember(ctx, taskID, self)
	if err != nil {
		c.JSON(consts.StatusInternalServerError, resp.Err(resp.CodeInternalServerError, err.Error()))
		return
	}
	if !isMember {
		c.JSON(consts.StatusForbidden, resp.Err(resp.CodeTaskNotMember, "not a task member"))
		return
	}

	task, err := h.repo.Get(ctx, taskID)
	if err != nil {
		if err == repo.ErrTaskV2NotFound {
			c.JSON(consts.StatusNotFound, resp.Err(resp.CodeNotFound, "task not found"))
			return
		}
		c.JSON(consts.StatusInternalServerError, resp.Err(resp.CodeInternalServerError, err.Error()))
		return
	}
	members, _ := h.repo.ListMembers(ctx, taskID)

	sinceSeq := -1
	if s := string(c.Query("since_seq")); s != "" {
		if v, err := strconv.Atoi(s); err == nil {
			sinceSeq = v
		}
	}
	limit := 100
	if s := string(c.Query("limit")); s != "" {
		if v, err := strconv.Atoi(s); err == nil && v > 0 {
			limit = v
		}
	}
	msgs, err := h.repo.ListMessages(ctx, taskID, sinceSeq, limit)
	if err != nil {
		c.JSON(consts.StatusInternalServerError, resp.Err(resp.CodeInternalServerError, err.Error()))
		return
	}

	c.JSON(consts.StatusOK, resp.OK(map[string]any{
		"task":     task,
		"members":  members,
		"messages": msgs,
	}))
}

// ListTasks GET /v2/tasks
// 列出自己参与的 task
func (h *TaskV2Handler) ListTasks(ctx context.Context, c *app.RequestContext) {
	self := c.GetString(ctxkey.AgentID)
	var status *model.TaskV2Status
	if s := string(c.Query("status")); s != "" {
		ts := model.TaskV2Status(s)
		status = &ts
	}
	limit := 50
	if s := string(c.Query("limit")); s != "" {
		if v, err := strconv.Atoi(s); err == nil && v > 0 {
			limit = v
		}
	}
	list, err := h.repo.ListByMember(ctx, self, status, limit)
	if err != nil {
		c.JSON(consts.StatusInternalServerError, resp.Err(resp.CodeInternalServerError, err.Error()))
		return
	}
	c.JSON(consts.StatusOK, resp.OK(list))
}

// CloseTask POST /v2/tasks/:task_id/close
func (h *TaskV2Handler) CloseTask(ctx context.Context, c *app.RequestContext) {
	self := c.GetString(ctxkey.AgentID)
	taskID := c.Param("task_id")
	if err := h.dispatcher.CloseTask(ctx, taskID, self); err != nil {
		if err == repo.ErrTaskV2NotFound {
			c.JSON(consts.StatusNotFound, resp.Err(resp.CodeNotFound, "task not found or not a member"))
			return
		}
		c.JSON(consts.StatusInternalServerError, resp.Err(resp.CodeInternalServerError, err.Error()))
		return
	}
	c.JSON(consts.StatusOK, resp.OK(nil))
}

// MarkRead POST /v2/tasks/:task_id/read
// body: {seq}
func (h *TaskV2Handler) MarkRead(ctx context.Context, c *app.RequestContext) {
	self := c.GetString(ctxkey.AgentID)
	taskID := c.Param("task_id")
	var req struct {
		Seq int `json:"seq"`
	}
	if err := c.BindJSON(&req); err != nil {
		c.JSON(consts.StatusBadRequest, resp.Err(resp.CodeBadRequest, "invalid body"))
		return
	}
	if err := h.repo.UpdateLastReadSeq(ctx, taskID, self, req.Seq); err != nil {
		c.JSON(consts.StatusInternalServerError, resp.Err(resp.CodeInternalServerError, err.Error()))
		return
	}
	c.JSON(consts.StatusOK, resp.OK(nil))
}
