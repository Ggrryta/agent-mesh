// Package mesh 实现 /v1/mesh/* 路由，由 agent 和本地 GAS daemon 使用。
//
// 两类端点：
//   - /auth/token：不走 JWT 中间件，用 API Key（用户分发给 agent）换短期 JWT。
//   - 其它业务端点：走 RequireAgent，只验 JWT，不碰 api_keys 表。
//
// 设计意图：业务请求零 DB 依赖认证；只有 /auth/token 每 agent 每小时走一次。
// 详见 ADR 007。
package mesh

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/Ggrryta/agent-mesh/gateway/internal/api/httpx"
	"github.com/Ggrryta/agent-mesh/gateway/internal/domain/agent"
	"github.com/Ggrryta/agent-mesh/gateway/internal/domain/apikey"
	"github.com/Ggrryta/agent-mesh/gateway/internal/domain/friendship"
	"github.com/Ggrryta/agent-mesh/gateway/internal/domain/group"
	"github.com/Ggrryta/agent-mesh/gateway/internal/domain/inbox"
	"github.com/Ggrryta/agent-mesh/gateway/internal/domain/outbox"
	"github.com/Ggrryta/agent-mesh/gateway/internal/domain/skill"
	"github.com/Ggrryta/agent-mesh/gateway/internal/domain/task"
	"github.com/Ggrryta/agent-mesh/gateway/internal/feed"
	"github.com/Ggrryta/agent-mesh/gateway/internal/middleware"
	"github.com/Ggrryta/agent-mesh/gateway/pkg/auth"
)

// AgentLookup 查询 agent 的 owner 信息（用于 FeedHub 推送）。
type AgentLookup interface {
	Lookup(ctx context.Context, agentID string) (ownerUID int64, kind string, found bool)
}

// Handler 持有 mesh API 的依赖。
type Handler struct {
	agents      *agent.Service
	keys        *apikey.Service     // 只被 /auth/token 端点使用
	tasks       *task.Service       // 可为 nil：未装配时 task 端点返 501
	inbox       *inbox.Service      // 可为 nil：未装配时 /inbox 返 501
	groups      *group.Service      // 可为 nil：未装配时群组端点返 501
	friends     *friendship.Service // 可为 nil：未装配时 friends 端点返 501
	outbox      outbox.Repo         // 可为 nil：未装配时群消息不写 outbox
	skills      skill.Repo          // 可为 nil：roster 不返回 skills
	signer      *auth.Signer
	feedHub     *feed.Hub    // 可为 nil：不推送前端事件
	agentLookup AgentLookup  // 可为 nil：不推送前端事件
}

// New 构造一个 Handler。keys / tasks / inboxSvc 可为 nil。
func New(agents *agent.Service, keys *apikey.Service, tasks *task.Service, inboxSvc *inbox.Service, signer *auth.Signer, opts ...MeshOption) *Handler {
	h := &Handler{agents: agents, keys: keys, tasks: tasks, inbox: inboxSvc, signer: signer}
	for _, o := range opts {
		o(h)
	}
	return h
}

// MeshOption 配置 mesh Handler 的可选依赖。
type MeshOption func(*Handler)

// WithFeed 注入 FeedHub。
func WithFeed(hub *feed.Hub) MeshOption { return func(h *Handler) { h.feedHub = hub } }

// WithAgentLookup 注入 agent 查询接口（用于推送时查 owner_uid）。
func WithAgentLookup(l AgentLookup) MeshOption { return func(h *Handler) { h.agentLookup = l } }

// WithGroups 注入群组 Service。
func WithGroups(svc *group.Service) MeshOption { return func(h *Handler) { h.groups = svc } }

// WithFriends 注入 friendship Service（mesh agent 查自己好友列表用）。
func WithFriends(svc *friendship.Service) MeshOption {
	return func(h *Handler) { h.friends = svc }
}

// WithOutbox 注入 outbox Repo（群消息 fan-out 用）。
func WithOutbox(repo outbox.Repo) MeshOption { return func(h *Handler) { h.outbox = repo } }

// WithSkills 注入 skill Repo（roster 返回 skills 用）。
func WithSkills(repo skill.Repo) MeshOption { return func(h *Handler) { h.skills = repo } }

// Mux 返回 mesh 路由。挂载方式：
// http.Handle("/v1/mesh/", http.StripPrefix("/v1/mesh", h.Mux()))。
func (h *Handler) Mux() http.Handler {
	mux := http.NewServeMux()

	// /auth/token：用 API Key 换 agent JWT。
	// 不走 RequireAgent —— 调用方此刻还没有 JWT。
	// Header X-Api-Key 承载原始 key，body JSON 里带 agent_id。
	mux.HandleFunc("POST /auth/token", h.handleIssueToken)

	// 注意：/agents/register 故意不挂到 mesh 层。初次注册走
	// admin /users/me/agents，让 gateway 能把注册和权限签发绑到一起。
	// 此后的更新走 mesh /agents/{id}/heartbeat 或 admin 接口。
	mux.Handle("POST /agents/{agent_id}/heartbeat", middleware.TrustGateway(http.HandlerFunc(h.handleHeartbeat)))
	mux.Handle("POST /agents/{agent_id}/drain", middleware.TrustGateway(http.HandlerFunc(h.handleDrain)))
	// agent 拉自己的完整配置：name / description / system_prompt / agent_card / skills 等。
	// GAS daemon 启动时用，把 system_prompt 注入 LLM 作为 system message。
	mux.Handle("GET /agents/me", middleware.TrustGateway(http.HandlerFunc(h.handleGetMe)))
	mux.Handle("GET /agents/me/friends", middleware.TrustGateway(http.HandlerFunc(h.handleListMyFriends)))
	mux.Handle("GET /agents/me/groups", middleware.TrustGateway(http.HandlerFunc(h.handleListMyGroups)))
	mux.Handle("GET /agents/{agent_id}/profile", middleware.TrustGateway(http.HandlerFunc(h.handleGetAgentProfile)))

	// Task API（详见 ADR 002 / 004）：Gateway 只持久化和路由，不执行任务。
	mux.Handle("POST /tasks", middleware.TrustGateway(http.HandlerFunc(h.handleSubmitTask)))
	mux.Handle("GET /tasks/{task_id}", middleware.TrustGateway(http.HandlerFunc(h.handleGetTask)))
	mux.Handle("GET /tasks", middleware.TrustGateway(http.HandlerFunc(h.handleListByContext)))
	mux.Handle("POST /tasks/{task_id}/messages", middleware.TrustGateway(http.HandlerFunc(h.handleAppendMessage)))
	mux.Handle("POST /tasks/{task_id}/artifacts", middleware.TrustGateway(http.HandlerFunc(h.handleAppendArtifact)))
	mux.Handle("POST /tasks/{task_id}/transition", middleware.TrustGateway(http.HandlerFunc(h.handleTransition)))

	// Inbox 拉取（详见 ADR 010）。MVP 是长轮询；SSE 留 Week 4。
	mux.Handle("GET /inbox", middleware.TrustGateway(http.HandlerFunc(h.handlePullInbox)))

	// 群组消息（详见 ADR 003）。
	// 群组协作（详见群组协作设计）：
	// - roster 让 agent 发现队友能力
	// - timeline 让 agent 查看 context 协作元数据
	mux.Handle("GET /groups/{group_id}/roster", middleware.TrustGateway(http.HandlerFunc(h.handleGetRoster)))
	mux.Handle("POST /groups/{group_id}/notify", middleware.TrustGateway(http.HandlerFunc(h.handleNotifyGroup)))
	mux.Handle("GET /tasks/context/{context_id}/timeline", middleware.TrustGateway(http.HandlerFunc(h.handleGetTimeline)))
	return mux
}

// enforceAgentID 把 JWT 里的 agent_id 与 path 参数做匹配，防止一个 agent
// 假冒另一个 agent 操作。
func enforceAgentID(r *http.Request) (string, int64, bool) {
	c := middleware.ClaimsFromContext(r.Context())
	if c == nil {
		return "", 0, false
	}
	pathID := r.PathValue("agent_id")
	if pathID != c.AgentID {
		return "", 0, false
	}
	return c.AgentID, c.UID, true
}

// ─── /auth/token ─────────────────────────────────────────────────────────

type issueTokenReq struct {
	AgentID string `json:"agent_id"`
}

type issueTokenResp struct {
	Token     string `json:"token"`
	ExpiresIn int    `json:"expires_in"` // 秒
	AgentID   string `json:"agent_id"`
}

// handleIssueToken 用 API Key + agent_id 换一枚短期 agent JWT。
//
// 流程：
//  1. 从 Header 取 X-Api-Key，做基础格式校验
//  2. apikey.Verify：hash 查 DB，判断是否吊销
//  3. 校验请求体里的 agent_id 确实属于该 key 的 owner
//  4. 签发 agent JWT（TTL = signer.AgentTTL()，默认 1h）
//
// 错误码与 SDK 重试契约（详见 docs/api.md）：
//   - 40111 token_invalid：API Key 格式错 / 不存在
//   - 40112 key_revoked：已被吊销
//   - 40113 agent_not_owned：agent_id 不属于该 key 的 owner
//   - 40001 bad_request：body 格式错 / 缺 agent_id
func (h *Handler) handleIssueToken(w http.ResponseWriter, r *http.Request) {
	if h.keys == nil {
		httpx.WriteError(w, http.StatusNotImplemented, httpx.CodeNotImpl, "apikey domain not wired")
		return
	}

	rawKey := r.Header.Get("X-Api-Key")
	if rawKey == "" {
		httpx.WriteError(w, http.StatusUnauthorized, httpx.CodeTokenInvalid, "missing X-Api-Key header")
		return
	}

	var req issueTokenReq
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, httpx.CodeBadRequest, "invalid request body")
		return
	}
	if req.AgentID == "" {
		httpx.WriteError(w, http.StatusBadRequest, httpx.CodeBadRequest, "agent_id is required")
		return
	}

	// Verify 内部会判格式 + 吊销态，按错误类型细化响应。
	key, err := h.keys.Verify(r.Context(), rawKey)
	if err != nil {
		switch {
		case errors.Is(err, apikey.ErrKeyInvalid), errors.Is(err, apikey.ErrKeyNotFound):
			httpx.WriteError(w, http.StatusUnauthorized, httpx.CodeTokenInvalid, "invalid api key")
		case errors.Is(err, apikey.ErrKeyRevoked):
			httpx.WriteError(w, http.StatusUnauthorized, httpx.CodeKeyRevoked, "api key revoked")
		default:
			httpx.WriteError(w, http.StatusInternalServerError, httpx.CodeInternal, err.Error())
		}
		return
	}

	// 校验 agent 归属：必须是该 key 的 owner 名下的 agent。
	// 这一步对每小时 1 次的 /auth/token 请求不构成性能压力。
	a, err := h.agents.Get(r.Context(), req.AgentID)
	if err != nil {
		if errors.Is(err, agent.ErrAgentNotFound) {
			// 刻意不把 "agent 不存在" 和 "agent 不是你的" 区分开，防止枚举。
			httpx.WriteError(w, http.StatusForbidden, httpx.CodeAgentNotOwned, "agent not owned by this api key")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, httpx.CodeInternal, err.Error())
		return
	}
	if a.OwnerUID != key.OwnerUID {
		httpx.WriteError(w, http.StatusForbidden, httpx.CodeAgentNotOwned, "agent not owned by this api key")
		return
	}

	tok, err := h.signer.IssueAgent(a.AgentID, key.OwnerUID, key.ID)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, httpx.CodeInternal, "issue jwt failed")
		return
	}

	httpx.WriteJSON(w, http.StatusOK, issueTokenResp{
		Token:     tok,
		ExpiresIn: int(h.signer.AgentTTL().Seconds()),
		AgentID:   a.AgentID,
	})
}

// ─── 业务端点 ────────────────────────────────────────────────────────────

type heartbeatResp struct {
	AgentID string `json:"agent_id"`
	Status  string `json:"status"`
}

func (h *Handler) handleHeartbeat(w http.ResponseWriter, r *http.Request) {
	agentID, uid, ok := enforceAgentID(r)
	if !ok {
		httpx.WriteError(w, http.StatusForbidden, httpx.CodeNotOwner, "agent_id mismatch")
		return
	}
	a, err := h.agents.Heartbeat(r.Context(), agentID, uid)
	if err != nil {
		if errors.Is(err, agent.ErrNotOwner) {
			httpx.WriteError(w, http.StatusForbidden, httpx.CodeNotOwner, "not the owner")
			return
		}
		if errors.Is(err, agent.ErrAgentNotFound) {
			httpx.WriteError(w, http.StatusNotFound, httpx.CodeNotFound, "agent not found")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, httpx.CodeInternal, err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, heartbeatResp{
		AgentID: a.AgentID,
		Status:  string(a.Status),
	})
}

func (h *Handler) handleDrain(w http.ResponseWriter, r *http.Request) {
	agentID, uid, ok := enforceAgentID(r)
	if !ok {
		httpx.WriteError(w, http.StatusForbidden, httpx.CodeNotOwner, "agent_id mismatch")
		return
	}
	if err := h.agents.Drain(r.Context(), agentID, uid); err != nil {
		if errors.Is(err, agent.ErrAgentNotFound) {
			httpx.WriteError(w, http.StatusNotFound, httpx.CodeNotFound, "agent not found")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, httpx.CodeInternal, err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"status": "draining"})
}

// handleGetMe 让 agent 通过 JWT 拉自己的完整配置。
// GAS daemon 启动时调用，把 system_prompt 作为 LLM system message 注入。
func (h *Handler) handleGetMe(w http.ResponseWriter, r *http.Request) {
	claims := middleware.ClaimsFromContext(r.Context())
	if claims == nil || claims.AgentID == "" {
		httpx.WriteError(w, http.StatusUnauthorized, httpx.CodeTokenInvalid, "missing agent claims")
		return
	}
	a, err := h.agents.Get(r.Context(), claims.AgentID)
	if err != nil {
		if errors.Is(err, agent.ErrAgentNotFound) {
			httpx.WriteError(w, http.StatusNotFound, httpx.CodeNotFound, "agent not found")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, httpx.CodeInternal, err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"agent_id":       a.AgentID,
		"name":           a.Name,
		"headline":       a.Headline,
		"description":    a.Description,
		"version":        a.Version,
		"system_prompt":  a.SystemPrompt,
		"workspace_path": a.WorkspacePath,
		"agent_card":     json.RawMessage(a.AgentCardJSON),
	})
}

// handleListMyFriends：让 agent 用 mesh JWT 拉自己的好友列表。
// 默认只返回 accepted；?status=pending 显式查 pending 等。
func (h *Handler) handleListMyFriends(w http.ResponseWriter, r *http.Request) {
	if h.friends == nil {
		httpx.WriteError(w, http.StatusNotImplemented, httpx.CodeNotImpl, "friendship service not wired")
		return
	}
	claims := middleware.ClaimsFromContext(r.Context())
	if claims == nil || claims.AgentID == "" {
		httpx.WriteError(w, http.StatusUnauthorized, httpx.CodeTokenInvalid, "missing agent claims")
		return
	}
	status := friendship.Status(r.URL.Query().Get("status"))
	if status == "" {
		status = friendship.StatusAccepted
	}
	list, err := h.friends.ListFriends(r.Context(), claims.UID, claims.AgentID, status)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, httpx.CodeBadRequest, err.Error())
		return
	}
	out := make([]map[string]any, 0, len(list))
	for _, f := range list {
		// "对方" 视角：如果 from 是自己，对方就是 to，反之亦然
		other := f.ToAgentID
		if f.ToAgentID == claims.AgentID {
			other = f.FromAgentID
		}
		out = append(out, map[string]any{
			"friend_agent_id": other,
			"friendship_id":   f.ID,
			"status":          string(f.Status),
		})
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"friends": out})
}

// handleListMyGroups：返回 agent 参与的群组列表（仅 group_id 列表，详情走 /groups/{id}/roster）。
func (h *Handler) handleListMyGroups(w http.ResponseWriter, r *http.Request) {
	if h.groups == nil {
		httpx.WriteError(w, http.StatusNotImplemented, httpx.CodeNotImpl, "group service not wired")
		return
	}
	claims := middleware.ClaimsFromContext(r.Context())
	if claims == nil || claims.AgentID == "" {
		httpx.WriteError(w, http.StatusUnauthorized, httpx.CodeTokenInvalid, "missing agent claims")
		return
	}
	ids, err := h.groups.GroupsOfAgent(r.Context(), claims.AgentID)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, httpx.CodeInternal, err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"group_ids": ids})
}

// publishFeed 向 agent 的 owner 推送前端事件。feedHub 或 agentLookup 为 nil 时静默跳过。
func (h *Handler) publishFeed(r *http.Request, eventType, agentID, taskID string, payload []byte) {
	if h.feedHub == nil || h.agentLookup == nil {
		return
	}
	ownerUID, _, found := h.agentLookup.Lookup(r.Context(), agentID)
	if !found {
		return
	}
	h.feedHub.Publish(r.Context(), ownerUID, &feed.FeedEvent{
		Type:    eventType,
		AgentID: agentID,
		TaskID:  taskID,
		Payload: payload,
	})
}

// handleNotifyGroup：向群组所有成员广播通知（不创建 task，不期望回复）。
func (h *Handler) handleNotifyGroup(w http.ResponseWriter, r *http.Request) {
	if h.groups == nil || h.inbox == nil {
		httpx.WriteError(w, http.StatusNotImplemented, httpx.CodeNotImpl, "group/inbox service not wired")
		return
	}
	claims := middleware.ClaimsFromContext(r.Context())
	if claims == nil || claims.AgentID == "" {
		httpx.WriteError(w, http.StatusUnauthorized, httpx.CodeTokenInvalid, "missing agent claims")
		return
	}
	groupID := r.PathValue("group_id")
	if groupID == "" {
		httpx.WriteError(w, http.StatusBadRequest, httpx.CodeBadRequest, "group_id required")
		return
	}

	// 验证调用者是群组成员
	isMember, err := h.groups.IsMember(r.Context(), groupID, claims.AgentID)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, httpx.CodeInternal, err.Error())
		return
	}
	if !isMember {
		httpx.WriteError(w, http.StatusForbidden, httpx.CodeForbidden, "not a member of this group")
		return
	}

	// 解析请求体
	var req struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Text == "" {
		httpx.WriteError(w, http.StatusBadRequest, httpx.CodeBadRequest, "text required")
		return
	}

	// 获取群组成员，排除发送者
	members, err := h.groups.ListMembers(r.Context(), groupID)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, httpx.CodeInternal, err.Error())
		return
	}

	notified := 0
	for _, m := range members {
		if m.AgentID == claims.AgentID {
			continue // 不通知自己
		}
		if err := h.inbox.EnqueueNotification(r.Context(), m.AgentID, claims.AgentID, groupID, req.Text); err != nil {
			continue // 单个失败不阻塞其他
		}
		notified++
	}

	httpx.WriteJSON(w, http.StatusOK, map[string]any{"notified": notified})
}

// handleGetAgentProfile：获取指定 agent 的 MeshAgentProfile（精简能力名片）。
// 调用方必须是目标 agent 的好友或同群成员。
func (h *Handler) handleGetAgentProfile(w http.ResponseWriter, r *http.Request) {
	claims := middleware.ClaimsFromContext(r.Context())
	if claims == nil || claims.AgentID == "" {
		httpx.WriteError(w, http.StatusUnauthorized, httpx.CodeTokenInvalid, "missing agent claims")
		return
	}
	targetID := r.PathValue("agent_id")
	if targetID == "" {
		httpx.WriteError(w, http.StatusBadRequest, httpx.CodeBadRequest, "agent_id required")
		return
	}

	// 权限校验：必须是好友或同群
	if h.friends != nil {
		ok, _ := h.friends.AreFriends(r.Context(), claims.AgentID, targetID)
		if !ok && h.groups != nil {
			ok, _ = h.groups.SameGroup(r.Context(), claims.AgentID, targetID)
		}
		if !ok {
			httpx.WriteError(w, http.StatusForbidden, httpx.CodeForbidden, "not a friend or group member")
			return
		}
	}

	a, err := h.agents.Get(r.Context(), targetID)
	if err != nil {
		httpx.WriteError(w, http.StatusNotFound, httpx.CodeNotFound, "agent not found")
		return
	}

	// 构造 profile
	profile := map[string]any{
		"agent_id":    a.AgentID,
		"name":        a.Name,
		"headline":    a.Headline,
		"description": a.Description,
		"status":      string(a.Status),
	}

	// 附加 skills
	if h.skills != nil {
		raw, _ := h.skills.ListByAgentIDs(r.Context(), []string{targetID})
		if ss, ok := raw[targetID]; ok && len(ss) > 0 {
			skills := make([]map[string]any, 0, len(ss))
			for _, s := range ss {
				skills = append(skills, map[string]any{
					"name":        s.Name,
					"description": s.Description,
					"tags":        s.Tags,
				})
			}
			profile["skills"] = skills
		}
	}

	httpx.WriteJSON(w, http.StatusOK, profile)
}
