package mesh

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Ggrryta/agent-mesh/gateway/internal/api/httpx"
	"github.com/Ggrryta/agent-mesh/gateway/internal/domain/inbox"
	"github.com/Ggrryta/agent-mesh/gateway/internal/domain/task"
	"github.com/Ggrryta/agent-mesh/gateway/internal/middleware"
	"github.com/Ggrryta/agent-mesh/gateway/internal/observability/metrics"
)

// ─── 请求 / 响应形 ───────────────────────────────────────────────

type messagePayload struct {
	MessageID  string         `json:"message_id"`
	Role       task.Role      `json:"role,omitempty"` // 对 AppendMessage 无效 —— 由 service 推断
	Parts      []task.Part    `json:"parts"`
	Preview    string         `json:"preview,omitempty"`
	Metadata   map[string]any `json:"metadata,omitempty"`
	RefTaskIDs []string       `json:"reference_task_ids,omitempty"`
}

type submitTaskReq struct {
	TaskID    string         `json:"task_id"`
	ContextID string         `json:"context_id,omitempty"`
	ToAgentID string         `json:"to_agent_id"`
	Message   messagePayload `json:"message"`
	Metadata  map[string]any `json:"metadata,omitempty"`
}

type taskResp struct {
	TaskID        string         `json:"task_id"`
	ContextID     string         `json:"context_id"`
	FromAgentID   string         `json:"from_agent_id"`
	ToAgentID     string         `json:"to_agent_id"`
	Status        task.State     `json:"status"`
	StatusMessage string         `json:"status_message,omitempty"`
	ErrorMsg      string         `json:"error,omitempty"`
	Metadata      map[string]any `json:"metadata,omitempty"`
	CreatedAt     string         `json:"created_at"`
	UpdatedAt     string         `json:"updated_at"`

	// 按需填充
	History   []*messageResp  `json:"history,omitempty"`
	Artifacts []*artifactResp `json:"artifacts,omitempty"`
}

type messageResp struct {
	MessageID  string         `json:"message_id"`
	TaskID     string         `json:"task_id"`
	ContextID  string         `json:"context_id"`
	Role       task.Role      `json:"role"`
	Parts      []task.Part    `json:"parts"`
	Metadata   map[string]any `json:"metadata,omitempty"`
	RefTaskIDs []string       `json:"reference_task_ids,omitempty"`
	CreatedAt  string         `json:"created_at"`
}

type artifactResp struct {
	ArtifactID  string         `json:"artifact_id"`
	TaskID      string         `json:"task_id"`
	ContextID   string         `json:"context_id"`
	Name        string         `json:"name,omitempty"`
	Description string         `json:"description,omitempty"`
	Parts       []task.Part    `json:"parts"`
	Metadata    map[string]any `json:"metadata,omitempty"`
	CreatedAt   string         `json:"created_at"`
}

type appendMessageReq struct {
	MessageID  string         `json:"message_id"`
	Parts      []task.Part    `json:"parts"`
	Preview    string         `json:"preview,omitempty"` // 可选：给群内旁观者看的摘要
	Metadata   map[string]any `json:"metadata,omitempty"`
	RefTaskIDs []string       `json:"reference_task_ids,omitempty"`
}

type appendArtifactReq struct {
	ArtifactID  string         `json:"artifact_id"`
	Name        string         `json:"name,omitempty"`
	Description string         `json:"description,omitempty"`
	Parts       []task.Part    `json:"parts"`
	Metadata    map[string]any `json:"metadata,omitempty"`
}

type transitionReq struct {
	ToState       task.State `json:"to_state"`
	StatusMessage string     `json:"status_message,omitempty"`
	Error         string     `json:"error,omitempty"`
}

// toTaskResp 把 domain Task + 可选 history/artifacts 组装成响应形。
func toTaskResp(t *task.Task, history []*task.Message, arts []*task.Artifact) *taskResp {
	r := &taskResp{
		TaskID:        t.TaskID,
		ContextID:     t.ContextID,
		FromAgentID:   t.FromAgentID,
		ToAgentID:     t.ToAgentID,
		Status:        t.Status,
		StatusMessage: t.StatusMessage,
		ErrorMsg:      t.ErrorMsg,
		Metadata:      t.Metadata,
		CreatedAt:     t.CreatedAt.UTC().Format("2006-01-02T15:04:05.000Z"),
		UpdatedAt:     t.UpdatedAt.UTC().Format("2006-01-02T15:04:05.000Z"),
	}
	if len(history) > 0 {
		r.History = make([]*messageResp, 0, len(history))
		for _, m := range history {
			r.History = append(r.History, toMessageResp(m))
		}
	}
	if len(arts) > 0 {
		r.Artifacts = make([]*artifactResp, 0, len(arts))
		for _, a := range arts {
			r.Artifacts = append(r.Artifacts, toArtifactResp(a))
		}
	}
	return r
}

func toMessageResp(m *task.Message) *messageResp {
	return &messageResp{
		MessageID: m.MessageID, TaskID: m.TaskID, ContextID: m.ContextID,
		Role: m.Role, Parts: m.Parts, Metadata: m.Metadata,
		RefTaskIDs: m.RefTaskIDs,
		CreatedAt:  m.CreatedAt.UTC().Format("2006-01-02T15:04:05.000Z"),
	}
}

func toArtifactResp(a *task.Artifact) *artifactResp {
	return &artifactResp{
		ArtifactID: a.ArtifactID, TaskID: a.TaskID, ContextID: a.ContextID,
		Name: a.Name, Description: a.Description,
		Parts: a.Parts, Metadata: a.Metadata,
		CreatedAt: a.CreatedAt.UTC().Format("2006-01-02T15:04:05.000Z"),
	}
}

// ─── Handler：Submit ──────────────────────────────────────────────

func (h *Handler) handleSubmitTask(w http.ResponseWriter, r *http.Request) {
	if h.tasks == nil {
		httpx.WriteError(w, http.StatusNotImplemented, httpx.CodeNotImpl, "task domain not wired")
		return
	}
	claims := middleware.ClaimsFromContext(r.Context())
	if claims == nil {
		httpx.WriteError(w, http.StatusUnauthorized, httpx.CodeTokenInvalid, "missing auth")
		return
	}
	var req submitTaskReq
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, httpx.CodeBadRequest, "invalid request body")
		return
	}
	created, err := h.tasks.Submit(r.Context(), task.SubmitInput{
		TaskID:      req.TaskID,
		ContextID:   req.ContextID,
		FromAgentID: claims.AgentID,
		ToAgentID:   req.ToAgentID,
		CallerUID:   claims.UID,
		Message: task.Message{
			MessageID:  req.Message.MessageID,
			Parts:      req.Message.Parts,
			Preview:    req.Message.Preview,
			Metadata:   req.Message.Metadata,
			RefTaskIDs: req.Message.RefTaskIDs,
		},
	})
	if err != nil {
		h.mapTaskError(w, err)
		return
	}
	metrics.TasksCreatedTotal.Inc()
	h.publishFeed(r, "task_created", claims.AgentID, created.TaskID, nil)
	h.publishFeed(r, "task_created", req.ToAgentID, created.TaskID, nil)
	httpx.WriteJSON(w, http.StatusCreated, toTaskResp(created, nil, nil))
}

// ─── Handler：Get / ListByContext ─────────────────────────────────

func (h *Handler) handleGetTask(w http.ResponseWriter, r *http.Request) {
	if h.tasks == nil {
		httpx.WriteError(w, http.StatusNotImplemented, httpx.CodeNotImpl, "task domain not wired")
		return
	}
	claims := middleware.ClaimsFromContext(r.Context())
	if claims == nil {
		httpx.WriteError(w, http.StatusUnauthorized, httpx.CodeTokenInvalid, "missing auth")
		return
	}
	taskID := r.PathValue("task_id")
	include := strings.Split(r.URL.Query().Get("include"), ",")
	withHistory := containsString(include, "history")
	withArts := containsString(include, "artifacts")

	t, history, arts, err := h.tasks.Get(r.Context(), claims.AgentID, claims.UID, taskID, withHistory, withArts)
	if err != nil {
		h.mapTaskError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toTaskResp(t, history, arts))
}

func (h *Handler) handleListByContext(w http.ResponseWriter, r *http.Request) {
	if h.tasks == nil {
		httpx.WriteError(w, http.StatusNotImplemented, httpx.CodeNotImpl, "task domain not wired")
		return
	}
	claims := middleware.ClaimsFromContext(r.Context())
	if claims == nil {
		httpx.WriteError(w, http.StatusUnauthorized, httpx.CodeTokenInvalid, "missing auth")
		return
	}
	ctxID := r.URL.Query().Get("context_id")
	if ctxID == "" {
		httpx.WriteError(w, http.StatusBadRequest, httpx.CodeBadRequest, "context_id is required")
		return
	}
	list, err := h.tasks.ListByContext(r.Context(), claims.AgentID, claims.UID, ctxID)
	if err != nil {
		h.mapTaskError(w, err)
		return
	}
	out := make([]*taskResp, 0, len(list))
	for _, t := range list {
		out = append(out, toTaskResp(t, nil, nil))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"tasks": out})
}

// ─── Handler：AppendMessage / AppendArtifact ──────────────────────

func (h *Handler) handleAppendMessage(w http.ResponseWriter, r *http.Request) {
	if h.tasks == nil {
		httpx.WriteError(w, http.StatusNotImplemented, httpx.CodeNotImpl, "task domain not wired")
		return
	}
	claims := middleware.ClaimsFromContext(r.Context())
	if claims == nil {
		httpx.WriteError(w, http.StatusUnauthorized, httpx.CodeTokenInvalid, "missing auth")
		return
	}
	var req appendMessageReq
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, httpx.CodeBadRequest, "invalid request body")
		return
	}
	m, err := h.tasks.AppendMessage(r.Context(), task.AppendMessageInput{
		TaskID:      r.PathValue("task_id"),
		CallerAgent: claims.AgentID,
		CallerUID:   claims.UID,
		MessageID:   req.MessageID,
		Parts:       req.Parts,
		Preview:     req.Preview,
		Metadata:    req.Metadata,
		RefTaskIDs:  req.RefTaskIDs,
	})
	if err != nil {
		h.mapTaskError(w, err)
		return
	}
	metrics.TaskMessagesTotal.Inc()
	h.publishFeed(r, "task_message", claims.AgentID, m.TaskID, nil)
	httpx.WriteJSON(w, http.StatusCreated, toMessageResp(m))
}

func (h *Handler) handleAppendArtifact(w http.ResponseWriter, r *http.Request) {
	if h.tasks == nil {
		httpx.WriteError(w, http.StatusNotImplemented, httpx.CodeNotImpl, "task domain not wired")
		return
	}
	claims := middleware.ClaimsFromContext(r.Context())
	if claims == nil {
		httpx.WriteError(w, http.StatusUnauthorized, httpx.CodeTokenInvalid, "missing auth")
		return
	}
	var req appendArtifactReq
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, httpx.CodeBadRequest, "invalid request body")
		return
	}
	a, err := h.tasks.AppendArtifact(r.Context(), task.AppendArtifactInput{
		TaskID:      r.PathValue("task_id"),
		CallerAgent: claims.AgentID,
		CallerUID:   claims.UID,
		ArtifactID:  req.ArtifactID,
		Name:        req.Name,
		Description: req.Description,
		Parts:       req.Parts,
		Metadata:    req.Metadata,
	})
	if err != nil {
		h.mapTaskError(w, err)
		return
	}
	metrics.TaskArtifactsTotal.Inc()
	h.publishFeed(r, "task_artifact", claims.AgentID, a.TaskID, nil)
	httpx.WriteJSON(w, http.StatusCreated, toArtifactResp(a))
}

// ─── Handler：Transition ─────────────────────────────────────────

func (h *Handler) handleTransition(w http.ResponseWriter, r *http.Request) {
	if h.tasks == nil {
		httpx.WriteError(w, http.StatusNotImplemented, httpx.CodeNotImpl, "task domain not wired")
		return
	}
	claims := middleware.ClaimsFromContext(r.Context())
	if claims == nil {
		httpx.WriteError(w, http.StatusUnauthorized, httpx.CodeTokenInvalid, "missing auth")
		return
	}
	var req transitionReq
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, httpx.CodeBadRequest, "invalid request body")
		return
	}
	updated, err := h.tasks.Transition(r.Context(), task.TransitionInput{
		TaskID:        r.PathValue("task_id"),
		CallerAgent:   claims.AgentID,
		CallerUID:     claims.UID,
		ToState:       req.ToState,
		StatusMessage: req.StatusMessage,
		ErrorMsg:      req.Error,
	})
	if err != nil {
		h.mapTaskError(w, err)
		return
	}
	metrics.TaskTransitionsTotal.WithLabelValues(string(req.ToState)).Inc()
	h.publishFeed(r, "task_transition", claims.AgentID, updated.TaskID, nil)
	httpx.WriteJSON(w, http.StatusOK, toTaskResp(updated, nil, nil))
}

// ─── Handler：Pull Inbox ─────────────────────────────────────────

type inboxEventResp struct {
	ID          int64           `json:"id"`
	Kind        inbox.Kind      `json:"kind"`
	TaskID      string          `json:"task_id"`
	RefID       string          `json:"ref_id"`
	Payload     json.RawMessage `json:"payload"`
	CreatedAt   string          `json:"created_at"`
	DeliveredAt string          `json:"delivered_at,omitempty"`
}

func (h *Handler) handlePullInbox(w http.ResponseWriter, r *http.Request) {
	if h.inbox == nil {
		httpx.WriteError(w, http.StatusNotImplemented, httpx.CodeNotImpl, "inbox domain not wired")
		return
	}
	claims := middleware.ClaimsFromContext(r.Context())
	if claims == nil {
		httpx.WriteError(w, http.StatusUnauthorized, httpx.CodeTokenInvalid, "missing auth")
		return
	}
	since, _ := strconv.ParseInt(r.URL.Query().Get("since"), 10, 64)
	if since < 0 {
		since = 0
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 {
		limit = 100
	}
	waitSec, _ := strconv.Atoi(r.URL.Query().Get("wait"))
	if waitSec < 0 {
		waitSec = 0
	}
	if waitSec > 30 {
		waitSec = 30
	}

	var (
		events []*inbox.Event
		maxID  int64
		err    error
	)
	if waitSec > 0 {
		events, maxID, err = h.inbox.PollWithWait(r.Context(), claims.AgentID, since, limit, time.Duration(waitSec)*time.Second)
	} else {
		events, maxID, err = h.inbox.Pull(r.Context(), claims.AgentID, since, limit)
	}
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, httpx.CodeInternal, err.Error())
		return
	}
	out := make([]inboxEventResp, 0, len(events))
	for _, e := range events {
		item := inboxEventResp{
			ID: e.ID, Kind: e.Kind, TaskID: e.TaskID, RefID: e.RefID,
			Payload:   e.Payload,
			CreatedAt: e.CreatedAt.UTC().Format("2006-01-02T15:04:05.000Z"),
		}
		if e.DeliveredAt != nil {
			item.DeliveredAt = e.DeliveredAt.UTC().Format("2006-01-02T15:04:05.000Z")
		}
		out = append(out, item)
	}
	metrics.InboxPullTotal.Inc()
	metrics.InboxPullEventsReturned.Observe(float64(len(out)))
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"events": out,
		"max_id": maxID,
	})
}

// ─── 错误映射 ─────────────────────────────────────────────────────

func (h *Handler) mapTaskError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, task.ErrTaskNotFound):
		httpx.WriteError(w, http.StatusNotFound, httpx.CodeNotFound, "task not found")
	case errors.Is(err, task.ErrMessageNotFound):
		httpx.WriteError(w, http.StatusNotFound, httpx.CodeNotFound, "message not found")
	case errors.Is(err, task.ErrNotParticipant):
		httpx.WriteError(w, http.StatusForbidden, httpx.CodeNotOwner, "not participant of the task")
	case errors.Is(err, task.ErrInvalidTransition):
		httpx.WriteError(w, http.StatusConflict, httpx.CodeConflict, "invalid state transition")
	case errors.Is(err, task.ErrMessageIDDuplicate):
		httpx.WriteError(w, http.StatusConflict, httpx.CodeConflict, "message_id conflict with different content")
	case errors.Is(err, task.ErrArtifactIDDuplicate):
		httpx.WriteError(w, http.StatusConflict, httpx.CodeConflict, "artifact_id already exists in task")
	case errors.Is(err, task.ErrInvalidTaskID),
		errors.Is(err, task.ErrInvalidMessageID),
		errors.Is(err, task.ErrInvalidArtifactID),
		errors.Is(err, task.ErrInvalidContextID),
		errors.Is(err, task.ErrInvalidPartKind),
		errors.Is(err, task.ErrInvalidRole):
		httpx.WriteError(w, http.StatusBadRequest, httpx.CodeBadRequest, err.Error())
	default:
		// 其它（AreFriends 失败 / virtual-user / 不是 owner 等业务错）
		// 走 400 —— 它们都是"caller 的请求本身有问题"而非服务器错误。
		httpx.WriteError(w, http.StatusBadRequest, httpx.CodeBadRequest, err.Error())
	}
}

// ─── helpers ──────────────────────────────────────────────────────

func containsString(list []string, target string) bool {
	for _, s := range list {
		if strings.TrimSpace(s) == target {
			return true
		}
	}
	return false
}

// 保留 import
var (
	_ = json.RawMessage{}
	_ = strconv.Atoi
	_ = inbox.KindMessage
)
