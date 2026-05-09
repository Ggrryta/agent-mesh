package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"time"

	"agent-gateway/internal/model"
	"agent-gateway/internal/repo"
	"agent-gateway/internal/service"
	"agent-gateway/pkg/ctxkey"
	"agent-gateway/pkg/logger"
	"agent-gateway/pkg/resp"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
	"go.uber.org/zap"
)

// MonitorHandler 为 Web UI 监控界面提供 3 个端点:
//
//   GET  /monitor/tasks                 我参与的所有 task 列表(分页)
//   GET  /monitor/tasks/:task_id/messages  某 task 的消息流(分页)
//   GET  /monitor/stream                SSE 长连接,推送新消息/task_created/task_closed
//
// 鉴权走 JWT(和 Web UI 其他页面一致),handler 从 ctx 取 caller app_id,
// 先查该 app_id 名下所有 agent,再据此过滤可见的 task/消息。
type MonitorHandler struct {
	agentRepo *repo.AgentRepo
	taskRepo  *repo.TaskV2Repo
	hub       *service.MonitorHub
}

func NewMonitorHandler(agentRepo *repo.AgentRepo, taskRepo *repo.TaskV2Repo, hub *service.MonitorHub) *MonitorHandler {
	return &MonitorHandler{agentRepo: agentRepo, taskRepo: taskRepo, hub: hub}
}

// listMyAgents 取当前 caller 名下所有 agent_id
func (h *MonitorHandler) listMyAgents(ctx context.Context, appID string) ([]string, error) {
	agents, _, err := h.agentRepo.List(ctx, repo.AgentFilter{
		OwnerAppID: appID,
		Page:       0, // 不分页
	})
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(agents))
	for _, a := range agents {
		ids = append(ids, a.AgentID)
	}
	return ids, nil
}

// ListMyTasks GET /monitor/tasks?status=active&limit=50
//
// 响应:
//  {
//    "tasks": [
//      {task_id, title, status, members: [...], last_message: {...}, updated_at, created_at, closed_at}
//    ]
//  }
//
// members 会标明哪些 agent 属于当前 caller(`mine: true`),便于前端显示 "我的 vs 对方"
func (h *MonitorHandler) ListMyTasks(ctx context.Context, c *app.RequestContext) {
	appID := c.GetString(ctxkey.AppID)
	if appID == "" {
		c.JSON(consts.StatusUnauthorized, resp.Err(resp.CodeUnauthorized, "unauthorized"))
		return
	}

	myAgents, err := h.listMyAgents(ctx, appID)
	if err != nil {
		c.JSON(consts.StatusInternalServerError, resp.Err(resp.CodeInternalServerError, err.Error()))
		return
	}
	if len(myAgents) == 0 {
		c.JSON(consts.StatusOK, resp.OK(map[string]any{"tasks": []any{}}))
		return
	}

	limit := 50
	if l := c.Query("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 && n <= 200 {
			limit = n
		}
	}
	var statusFilter *model.TaskV2Status
	if s := c.Query("status"); s != "" {
		sv := model.TaskV2Status(s)
		statusFilter = &sv
	}

	// 去重用 map,因为不同 agent 都参与同一 task 时 ListByMember 会返回多遍
	seenTask := make(map[string]struct{})
	taskInfos := make([]map[string]any, 0)

	for _, ag := range myAgents {
		tasks, err := h.taskRepo.ListByMember(ctx, ag, statusFilter, limit)
		if err != nil {
			continue // 单个 agent 查错不阻塞整体
		}
		for _, t := range tasks {
			if _, ok := seenTask[t.TaskID]; ok {
				continue
			}
			seenTask[t.TaskID] = struct{}{}

			// 取该 task 全部成员
			members, _ := h.taskRepo.ListMembers(ctx, t.TaskID)
			memberInfos := make([]map[string]any, 0, len(members))
			mineSet := make(map[string]struct{}, len(myAgents))
			for _, a := range myAgents {
				mineSet[a] = struct{}{}
			}
			for _, m := range members {
				_, isMine := mineSet[m.AgentID]
				memberInfos = append(memberInfos, map[string]any{
					"agent_id":      m.AgentID,
					"role":          m.Role,
					"mine":          isMine,
					"last_read_seq": m.LastReadSeq,
				})
			}

			// 最后一条消息预览
			lastMsgs, _ := h.taskRepo.ListMessages(ctx, t.TaskID, -1, 1)
			var lastMsg map[string]any
			if len(lastMsgs) > 0 {
				m := lastMsgs[len(lastMsgs)-1]
				preview := firstTextPart(m.Content)
				if len(preview) > 120 {
					preview = preview[:120] + "..."
				}
				lastMsg = map[string]any{
					"seq":             m.Seq,
					"sender_agent_id": m.SenderAgentID,
					"preview":         preview,
					"created_at":      m.CreatedAt,
				}
			}

			taskInfos = append(taskInfos, map[string]any{
				"task_id":          t.TaskID,
				"title":            t.Title,
				"status":           t.Status,
				"creator_agent_id": t.CreatorAgentID,
				"members":          memberInfos,
				"last_message":     lastMsg,
				"expire_at":        t.ExpireAt,
				"created_at":       t.CreatedAt,
				"updated_at":       t.UpdatedAt,
				"closed_at":        t.ClosedAt,
			})
		}
	}

	// 按 updated_at 倒序(最新活动在前)
	sortTasksByUpdatedDesc(taskInfos)

	c.JSON(consts.StatusOK, resp.OK(map[string]any{"tasks": taskInfos}))
}

// GetTaskMessages GET /monitor/tasks/:task_id/messages?since_seq=-1&limit=50
//
// 响应:{messages: [{seq, sender_agent_id, message_id, parts, created_at}]}
// 权限:caller 名下的任何 agent 在该 task 里即可查看。
func (h *MonitorHandler) GetTaskMessages(ctx context.Context, c *app.RequestContext) {
	appID := c.GetString(ctxkey.AppID)
	if appID == "" {
		c.JSON(consts.StatusUnauthorized, resp.Err(resp.CodeUnauthorized, "unauthorized"))
		return
	}
	taskID := c.Param("task_id")
	if taskID == "" {
		c.JSON(consts.StatusBadRequest, resp.Err(resp.CodeBadRequest, "task_id required"))
		return
	}

	// 校验 caller 名下至少一个 agent 是该 task 成员
	myAgents, err := h.listMyAgents(ctx, appID)
	if err != nil {
		c.JSON(consts.StatusInternalServerError, resp.Err(resp.CodeInternalServerError, err.Error()))
		return
	}
	members, err := h.taskRepo.ListMembers(ctx, taskID)
	if err != nil {
		c.JSON(consts.StatusInternalServerError, resp.Err(resp.CodeInternalServerError, err.Error()))
		return
	}
	memberSet := make(map[string]struct{}, len(members))
	for _, m := range members {
		memberSet[m.AgentID] = struct{}{}
	}
	hasAccess := false
	for _, a := range myAgents {
		if _, ok := memberSet[a]; ok {
			hasAccess = true
			break
		}
	}
	if !hasAccess {
		c.JSON(consts.StatusForbidden, resp.Err(resp.CodeForbidden, "not a member of this task"))
		return
	}

	sinceSeq := -1
	if s := c.Query("since_seq"); s != "" {
		if n, err := strconv.Atoi(s); err == nil {
			sinceSeq = n
		}
	}
	limit := 50
	if l := c.Query("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 && n <= 200 {
			limit = n
		}
	}

	msgs, err := h.taskRepo.ListMessages(ctx, taskID, sinceSeq, limit)
	if err != nil {
		c.JSON(consts.StatusInternalServerError, resp.Err(resp.CodeInternalServerError, err.Error()))
		return
	}

	out := make([]map[string]any, 0, len(msgs))
	for _, m := range msgs {
		var parts any
		_ = json.Unmarshal(m.Content, &parts)
		out = append(out, map[string]any{
			"seq":             m.Seq,
			"sender_agent_id": m.SenderAgentID,
			"message_id":      m.MessageID,
			"parts":           parts,
			"created_at":      m.CreatedAt,
		})
	}
	c.JSON(consts.StatusOK, resp.OK(map[string]any{"messages": out}))
}

// Stream GET /monitor/stream
// SSE 长连接,推送该 caller 名下所有 agent 参与 task 的新消息/task_created/task_closed 事件。
func (h *MonitorHandler) Stream(ctx context.Context, c *app.RequestContext) {
	appID := c.GetString(ctxkey.AppID)
	if appID == "" {
		c.JSON(consts.StatusUnauthorized, resp.Err(resp.CodeUnauthorized, "unauthorized"))
		return
	}
	myAgents, err := h.listMyAgents(ctx, appID)
	if err != nil {
		c.JSON(consts.StatusInternalServerError, resp.Err(resp.CodeInternalServerError, err.Error()))
		return
	}

	// 生成会话 ID。用时间戳 + caller 足够区分同一 app 的多 tab。
	sessionID := fmt.Sprintf("%s-%d", appID, time.Now().UnixNano())

	session := h.hub.Subscribe(sessionID, appID, myAgents)

	c.Response.Header.Set("Content-Type", "text/event-stream")
	c.Response.Header.Set("Cache-Control", "no-cache")
	c.Response.Header.Set("Connection", "keep-alive")
	c.Response.Header.Set("X-Accel-Buffering", "no")
	c.Response.SetStatusCode(consts.StatusOK)

	pr, pw := io.Pipe()
	c.SetBodyStream(pr, -1)

	go func() {
		defer func() {
			if r := recover(); r != nil {
				logger.Error("monitor stream panic", zap.Any("recover", r))
			}
			h.hub.Unsubscribe(session)
			pw.Close()
		}()

		logger.Info("monitor stream opened",
			zap.String("session_id", sessionID),
			zap.String("app_id", appID),
			zap.Int("my_agents", len(myAgents)))

		// hello event
		if _, err := pw.Write(encodeMonitorSSE(&service.MonitorEvent{
			Kind: service.MonitorEventPing,
			Data: map[string]any{"hello": appID, "my_agents": myAgents},
		})); err != nil {
			return
		}

		for {
			select {
			case <-ctx.Done():
				logger.Info("monitor stream: client disconnected",
					zap.String("session_id", sessionID))
				return
			case <-session.Done:
				return
			case evt, ok := <-session.Events:
				if !ok {
					return
				}
				if _, err := pw.Write(encodeMonitorSSE(evt)); err != nil {
					logger.Info("monitor stream: write failed",
						zap.String("session_id", sessionID), zap.Error(err))
					return
				}
			}
		}
	}()
}

// encodeMonitorSSE 把 MonitorEvent 序列化为 SSE 帧
func encodeMonitorSSE(e *service.MonitorEvent) []byte {
	data, err := json.Marshal(e)
	if err != nil {
		return []byte(fmt.Sprintf(": marshal error %v\n\n", err))
	}
	// 标准 SSE: event: <kind>\ndata: <json>\n\n
	return []byte(fmt.Sprintf("event: %s\ndata: %s\n\n", e.Kind, data))
}

// firstTextPart 从 A2A parts JSON 里取第一个 text 段,给 task 列表的预览用。
// parts 形如 [{"kind":"text","text":"..."}, ...]
func firstTextPart(content []byte) string {
	var arr []map[string]any
	if err := json.Unmarshal(content, &arr); err != nil {
		return ""
	}
	for _, p := range arr {
		if k, _ := p["kind"].(string); k == "text" {
			if t, _ := p["text"].(string); t != "" {
				return t
			}
		}
	}
	return ""
}

// sortTasksByUpdatedDesc 按 task 的 last_message.created_at(无则 updated_at)倒序
func sortTasksByUpdatedDesc(tasks []map[string]any) {
	ts := func(t map[string]any) time.Time {
		if lm, ok := t["last_message"].(map[string]any); ok && lm != nil {
			if ct, ok := lm["created_at"].(time.Time); ok {
				return ct
			}
		}
		if ut, ok := t["updated_at"].(time.Time); ok {
			return ut
		}
		return time.Time{}
	}
	// 简单冒泡(数量一般不大,任务量 <=100),避免引入 sort.Slice 的 closure 分配
	for i := 0; i < len(tasks); i++ {
		for j := i + 1; j < len(tasks); j++ {
			if ts(tasks[j]).After(ts(tasks[i])) {
				tasks[i], tasks[j] = tasks[j], tasks[i]
			}
		}
	}
}
