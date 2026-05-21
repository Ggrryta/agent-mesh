package admin

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Ggrryta/agent-mesh/gateway/internal/api/httpx"
	"github.com/Ggrryta/agent-mesh/gateway/internal/domain/task"
	"github.com/Ggrryta/agent-mesh/gateway/internal/domain/user"
	"github.com/Ggrryta/agent-mesh/gateway/internal/middleware"
)

// ─── Admin Task API（用户通过前端给 agent 下令）─────────────────

type adminSubmitTaskReq struct {
	ToAgentID string `json:"to_agent_id"`
	Message   struct {
		MessageID string      `json:"message_id"`
		Parts     []task.Part `json:"parts"`
		Preview   string      `json:"preview,omitempty"`
	} `json:"message"`
}

func (h *Handler) handleAdminSubmitTask(w http.ResponseWriter, r *http.Request) {
	if h.tasks == nil {
		httpx.WriteError(w, http.StatusNotImplemented, httpx.CodeNotImpl, "task domain not wired")
		return
	}
	uid, ok := middleware.UIDFromContext(r.Context())
	if !ok {
		httpx.WriteError(w, http.StatusUnauthorized, httpx.CodeTokenInvalid, "missing auth")
		return
	}

	var req adminSubmitTaskReq
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, httpx.CodeBadRequest, "invalid body")
		return
	}
	if req.ToAgentID == "" || req.Message.MessageID == "" || len(req.Message.Parts) == 0 {
		httpx.WriteError(w, http.StatusBadRequest, httpx.CodeBadRequest, "to_agent_id, message.message_id, message.parts required")
		return
	}

	virtualAgentID := user.VirtualAgentIDFor(uid)

	// 用户前端下令时不指定 task_id/context_id；Gateway 自动生成。
	taskID := fmt.Sprintf("t-admin-%d-%d", uid, time.Now().UnixNano())

	created, err := h.tasks.Submit(r.Context(), task.SubmitInput{
		TaskID:      taskID,
		FromAgentID: virtualAgentID,
		ToAgentID:   req.ToAgentID,
		CallerUID:   uid,
		Message: task.Message{
			MessageID: req.Message.MessageID,
			Parts:     req.Message.Parts,
			Preview:   req.Message.Preview,
		},
	})
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, httpx.CodeBadRequest, err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, map[string]any{
		"task_id":    created.TaskID,
		"context_id": created.ContextID,
		"status":     string(created.Status),
	})
}

func (h *Handler) handleAdminGetTask(w http.ResponseWriter, r *http.Request) {
	if h.tasks == nil {
		httpx.WriteError(w, http.StatusNotImplemented, httpx.CodeNotImpl, "task domain not wired")
		return
	}
	uid, ok := middleware.UIDFromContext(r.Context())
	if !ok {
		httpx.WriteError(w, http.StatusUnauthorized, httpx.CodeTokenInvalid, "missing auth")
		return
	}
	taskID := r.PathValue("task_id")

	include := strings.Split(r.URL.Query().Get("include"), ",")
	withHistory := containsStr(include, "history")
	withArts := containsStr(include, "artifacts")

	// 用户视角的 Get：只要 task 任意一端的 agent 属于 caller uid 就放行，
	// 不强制 caller 一定是 virtual-user。这样用户能看自己 agent 之间互发的 task 历史。
	t, history, arts, err := h.tasks.GetForUser(r.Context(), uid, taskID, withHistory, withArts)
	if err != nil {
		if errors.Is(err, task.ErrTaskNotFound) {
			httpx.WriteError(w, http.StatusNotFound, httpx.CodeNotFound, "task not found")
			return
		}
		httpx.WriteError(w, http.StatusBadRequest, httpx.CodeBadRequest, err.Error())
		return
	}

	resp := map[string]any{
		"task_id":    t.TaskID,
		"context_id": t.ContextID,
		"from":       t.FromAgentID,
		"to":         t.ToAgentID,
		"status":     string(t.Status),
		"created_at": t.CreatedAt.UTC().Format("2006-01-02T15:04:05.000Z"),
	}
	if withHistory && len(history) > 0 {
		resp["history"] = history
		// 如果消息条数恰好等于硬上限，可能被截：提示前端用户去用 timeline 查看完整历史。
		if len(history) >= task.MaxMessageHistoryRows {
			resp["history_truncated"] = true
		}
	}
	if withArts && len(arts) > 0 {
		resp["artifacts"] = arts
	}
	httpx.WriteJSON(w, http.StatusOK, resp)
}

// handleAdminAppendMessage：用户从前端给已有 task 继续发消息。
//
// 跟 mesh /tasks/:id/messages 是同一个 service.AppendMessage，
// 但 caller 是 user（virtual-user-{uid}），不是 agent。
type adminAppendMessageReq struct {
	MessageID string      `json:"message_id"`
	Parts     []task.Part `json:"parts"`
	Preview   string      `json:"preview,omitempty"`
}

func (h *Handler) handleAdminAppendMessage(w http.ResponseWriter, r *http.Request) {
	if h.tasks == nil {
		httpx.WriteError(w, http.StatusNotImplemented, httpx.CodeNotImpl, "task domain not wired")
		return
	}
	uid, ok := middleware.UIDFromContext(r.Context())
	if !ok {
		httpx.WriteError(w, http.StatusUnauthorized, httpx.CodeTokenInvalid, "missing auth")
		return
	}
	taskID := r.PathValue("task_id")
	if taskID == "" {
		httpx.WriteError(w, http.StatusBadRequest, httpx.CodeBadRequest, "task_id required")
		return
	}

	var req adminAppendMessageReq
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, httpx.CodeBadRequest, "invalid body")
		return
	}
	if req.MessageID == "" || len(req.Parts) == 0 {
		httpx.WriteError(w, http.StatusBadRequest, httpx.CodeBadRequest, "message_id, parts required")
		return
	}

	// caller agent = virtual-user-{uid}：service 会判断该用户是不是 task.from/to 的 owner
	virtualAgentID := user.VirtualAgentIDFor(uid)
	saved, err := h.tasks.AppendMessage(r.Context(), task.AppendMessageInput{
		TaskID:      taskID,
		CallerAgent: virtualAgentID,
		CallerUID:   uid,
		MessageID:   req.MessageID,
		Parts:       req.Parts,
		Preview:     req.Preview,
	})
	if err != nil {
		if errors.Is(err, task.ErrTaskNotFound) {
			httpx.WriteError(w, http.StatusNotFound, httpx.CodeNotFound, "task not found")
			return
		}
		httpx.WriteError(w, http.StatusBadRequest, httpx.CodeBadRequest, err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, map[string]any{
		"message_id": saved.MessageID,
		"task_id":    saved.TaskID,
		"role":       string(saved.Role),
		"created_at": saved.CreatedAt.UTC().Format("2006-01-02T15:04:05.000Z"),
	})
}

// handleAdminListTasks 列出用户的 task。
//
// 两种模式：
//  1. 显式 context_id（且不是 "*"）：按 task.ListByContext 拉单个 context 内全部 task，
//     用于"任务详情"等聚焦视图。授权要求 caller 是 from/to 之一。
//  2. context_id 缺失 / "*"：列出该 user 名下全部 agent（含 virtual-user-{uid}）参与的
//     recent task，给"我的任务列表"页用。limit 默认 50。
func (h *Handler) handleAdminListTasks(w http.ResponseWriter, r *http.Request) {
	if h.tasks == nil {
		httpx.WriteError(w, http.StatusNotImplemented, httpx.CodeNotImpl, "task domain not wired")
		return
	}
	uid, ok := middleware.UIDFromContext(r.Context())
	if !ok {
		httpx.WriteError(w, http.StatusUnauthorized, httpx.CodeTokenInvalid, "missing auth")
		return
	}
	contextID := r.URL.Query().Get("context_id")
	limit := parsePositiveInt(r.URL.Query().Get("limit"), 50)
	if limit > 200 {
		limit = 200
	}

	// 模式 2：列出该用户全部 agent 参与的 task
	if contextID == "" || contextID == "*" {
		// 收集当前 user 的所有 agent_id（含虚拟 user agent）
		myAgents, err := h.agents.ListByOwner(r.Context(), uid)
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, httpx.CodeInternal, err.Error())
			return
		}
		agentIDs := make([]string, 0, len(myAgents)+1)
		for _, a := range myAgents {
			agentIDs = append(agentIDs, a.AgentID)
		}
		// 虚拟 user-agent：UI 通过它向其他 agent 发任务，必须包含在内
		agentIDs = append(agentIDs, user.VirtualAgentIDFor(uid))

		list, err := h.tasks.ListRecentByAgents(r.Context(), agentIDs, limit)
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, httpx.CodeInternal, err.Error())
			return
		}
		out := make([]map[string]any, 0, len(list))
		for _, t := range list {
			out = append(out, map[string]any{
				"task_id": t.TaskID, "context_id": t.ContextID,
				"from": t.FromAgentID, "to": t.ToAgentID,
				"status":     string(t.Status),
				"created_at": t.CreatedAt.Format(time.RFC3339),
				"updated_at": t.UpdatedAt.Format(time.RFC3339),
			})
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"tasks": out})
		return
	}

	// 模式 1：按 context_id 聚焦
	agentID := r.URL.Query().Get("agent")
	if agentID == "" {
		agentID = user.VirtualAgentIDFor(uid)
	}

	list, err := h.tasks.ListByContext(r.Context(), agentID, uid, contextID)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, httpx.CodeBadRequest, err.Error())
		return
	}
	out := make([]map[string]any, 0, len(list))
	for _, t := range list {
		out = append(out, map[string]any{
			"task_id": t.TaskID, "context_id": t.ContextID,
			"from": t.FromAgentID, "to": t.ToAgentID,
			"status":     string(t.Status),
			"created_at": t.CreatedAt.Format(time.RFC3339),
			"updated_at": t.UpdatedAt.Format(time.RFC3339),
		})
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"tasks": out})
}

func containsStr(list []string, target string) bool {
	for _, s := range list {
		if strings.TrimSpace(s) == target {
			return true
		}
	}
	return false
}

// handleAdminGetTimeline 用户前端拉 context 的元数据时间轴（协作过程可视化）。
func (h *Handler) handleAdminGetTimeline(w http.ResponseWriter, r *http.Request) {
	if h.tasks == nil {
		httpx.WriteError(w, http.StatusNotImplemented, httpx.CodeNotImpl, "task domain not wired")
		return
	}
	_, ok := middleware.UIDFromContext(r.Context())
	if !ok {
		httpx.WriteError(w, http.StatusUnauthorized, httpx.CodeTokenInvalid, "missing auth")
		return
	}
	contextID := r.PathValue("context_id")
	sinceID, _ := strconv.ParseInt(r.URL.Query().Get("since"), 10, 64)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))

	entries, err := h.tasks.ListTimeline(r.Context(), contextID, sinceID, limit)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, httpx.CodeBadRequest, err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"entries": entries})
}

// handleAdminCleanupTasks 清理用户名下的终态 task（completed/failed）。
// Query params:
//   - before_days: 清理多少天前的 task（必填，1-365）
//
// 删除 task 主表 + 关联的 messages + artifacts + inbox events。
func (h *Handler) handleAdminCleanupTasks(w http.ResponseWriter, r *http.Request) {
	if h.tasks == nil {
		httpx.WriteError(w, http.StatusNotImplemented, httpx.CodeNotImpl, "task domain not wired")
		return
	}
	uid, ok := middleware.UIDFromContext(r.Context())
	if !ok {
		httpx.WriteError(w, http.StatusUnauthorized, httpx.CodeTokenInvalid, "missing auth")
		return
	}

	daysStr := r.URL.Query().Get("before_days")
	days, err := strconv.Atoi(daysStr)
	if err != nil || days < 1 || days > 365 {
		httpx.WriteError(w, http.StatusBadRequest, httpx.CodeBadRequest, "before_days must be 1-365")
		return
	}

	before := time.Now().Add(-time.Duration(days) * 24 * time.Hour)
	deleted, err := h.tasks.CleanupTerminalTasks(r.Context(), uid, before)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, httpx.CodeInternal, err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"deleted": deleted})
}

// handleAdminDeleteTask 删除单个 task（用户自选删除）。
// 只允许删除自己名下 agent 参与的 task。
func (h *Handler) handleAdminDeleteTask(w http.ResponseWriter, r *http.Request) {
	if h.tasks == nil {
		httpx.WriteError(w, http.StatusNotImplemented, httpx.CodeNotImpl, "task domain not wired")
		return
	}
	uid, ok := middleware.UIDFromContext(r.Context())
	if !ok {
		httpx.WriteError(w, http.StatusUnauthorized, httpx.CodeTokenInvalid, "missing auth")
		return
	}
	taskID := r.PathValue("task_id")

	err := h.tasks.DeleteTask(r.Context(), uid, taskID)
	if err != nil {
		if errors.Is(err, task.ErrTaskNotFound) || errors.Is(err, task.ErrNotParticipant) {
			httpx.WriteError(w, http.StatusNotFound, httpx.CodeNotFound, "task not found")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, httpx.CodeInternal, err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
}
