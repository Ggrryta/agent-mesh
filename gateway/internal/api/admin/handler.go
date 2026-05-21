// Package admin 实现 /v1/admin/* 路由，web 控制台走这里。
// 除了 /auth/* 之外，所有端点都要求有效的 user JWT。
package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Ggrryta/agent-mesh/gateway/internal/api/httpx"
	"github.com/Ggrryta/agent-mesh/gateway/internal/domain/agent"
	"github.com/Ggrryta/agent-mesh/gateway/internal/domain/apikey"
	"github.com/Ggrryta/agent-mesh/gateway/internal/domain/friendship"
	"github.com/Ggrryta/agent-mesh/gateway/internal/domain/group"
	"github.com/Ggrryta/agent-mesh/gateway/internal/domain/publication"
	"github.com/Ggrryta/agent-mesh/gateway/internal/domain/skill"
	"github.com/Ggrryta/agent-mesh/gateway/internal/domain/task"
	"github.com/Ggrryta/agent-mesh/gateway/internal/domain/user"
	"github.com/Ggrryta/agent-mesh/gateway/internal/feed"
	"github.com/Ggrryta/agent-mesh/gateway/internal/middleware"
	"github.com/Ggrryta/agent-mesh/gateway/pkg/auth"

	"go.uber.org/zap"
)

// Handler 是 admin API 的入口。service 依赖放这里，Mux 方法把它们挂到路由上。
type Handler struct {
	users        *user.Service
	agents       *agent.Service
	skills       skill.Repo
	keys         *apikey.Service
	friends      *friendship.Service
	groups       *group.Service
	tasks        *task.Service
	feed         *feed.Hub
	publications *publication.Service
	signer       *auth.Signer
	log          *zap.Logger
}

// New 构造 Handler。skills / keys / friends / feedHub 允许为 nil（域未接入时），
// 对应端点会回 501 或 503。
func New(
	users *user.Service,
	agents *agent.Service,
	skills skill.Repo,
	keys *apikey.Service,
	friends *friendship.Service,
	signer *auth.Signer,
	opts ...Option,
) *Handler {
	h := &Handler{
		users: users, agents: agents, skills: skills,
		keys: keys, friends: friends, signer: signer,
		log: zap.NewNop(),
	}
	for _, o := range opts {
		o(h)
	}
	return h
}

// Option 配置 Handler 的可选依赖。
type Option func(*Handler)

// WithFeed 注入 FeedHub。
func WithFeed(hub *feed.Hub) Option { return func(h *Handler) { h.feed = hub } }

// WithLogger 注入 logger。
func WithLogger(log *zap.Logger) Option { return func(h *Handler) { h.log = log } }

// WithGroups 注入群组 Service。
func WithGroups(svc *group.Service) Option { return func(h *Handler) { h.groups = svc } }

// WithTasks 注入 Task Service。
func WithTasks(svc *task.Service) Option { return func(h *Handler) { h.tasks = svc } }

// WithPublications 注入 Market publication Service。未注入时 /publications/* 回 501。
func WithPublications(svc *publication.Service) Option {
	return func(h *Handler) { h.publications = svc }
}

// Mux 返回挂在 /v1/admin/ 下的路由。调用方用
// http.Handle("/v1/admin/", http.StripPrefix("/v1/admin", h.Mux())) 挂载。
func (h *Handler) Mux() http.Handler {
	mux := http.NewServeMux()
	// auth 端点对外开放。Register / Login 负责签发 JWT 解锁其它端点。
	mux.HandleFunc("POST /auth/register", h.handleRegister)
	mux.HandleFunc("POST /auth/login", h.handleLogin)

	// user scope 的端点。
	mux.Handle("GET /users/me", middleware.TrustGateway(http.HandlerFunc(h.handleMe)))
	mux.Handle("GET /users/me/stats", middleware.TrustGateway(http.HandlerFunc(h.handleMyStats)))
	mux.Handle("GET /users/me/dashboard", middleware.TrustGateway(http.HandlerFunc(h.handleMyDashboard)))

	// agent 管理（user scope：owner 管自己的 agent）。
	mux.Handle("POST /users/me/agents", middleware.TrustGateway(http.HandlerFunc(h.handleCreateAgent)))
	mux.Handle("GET /users/me/agents", middleware.TrustGateway(http.HandlerFunc(h.handleListMyAgents)))
	mux.Handle("GET /agents/{agent_id}", middleware.TrustGateway(http.HandlerFunc(h.handleGetAgent)))
	mux.Handle("GET /agents/{agent_id}/skills", middleware.TrustGateway(http.HandlerFunc(h.handleListSkills)))
	mux.Handle("POST /agents/{agent_id}/drain", middleware.TrustGateway(http.HandlerFunc(h.handleDrainAgent)))
	mux.Handle("DELETE /agents/{agent_id}", middleware.TrustGateway(http.HandlerFunc(h.handleDeleteAgent)))

	// API Key 管理（user scope，不涉及具体 agent）。
	// key 是用户持有的长期凭证，用户自行分发给名下的 agent。
	mux.Handle("POST /users/me/api-keys", middleware.TrustGateway(http.HandlerFunc(h.handleCreateAPIKey)))
	mux.Handle("GET /users/me/api-keys", middleware.TrustGateway(http.HandlerFunc(h.handleListAPIKeys)))
	mux.Handle("DELETE /users/me/api-keys/{key_id}", middleware.TrustGateway(http.HandlerFunc(h.handleRevokeAPIKey)))

	// Friendship：粒度是 agent ↔ agent，但所有操作都以 **owner** 身份进行。
	// 详见 ADR 008。
	mux.Handle("POST /friends", middleware.TrustGateway(http.HandlerFunc(h.handleFriendRequest)))
	mux.Handle("GET /agents/{agent_id}/friends", middleware.TrustGateway(http.HandlerFunc(h.handleListFriends)))
	mux.Handle("GET /agents/{agent_id}/friends/incoming", middleware.TrustGateway(http.HandlerFunc(h.handleListIncoming)))
	mux.Handle("POST /friends/{id}/accept", middleware.TrustGateway(http.HandlerFunc(h.handleFriendAccept)))
	mux.Handle("POST /friends/{id}/reject", middleware.TrustGateway(http.HandlerFunc(h.handleFriendReject)))
	mux.Handle("POST /friends/{id}/revoke", middleware.TrustGateway(http.HandlerFunc(h.handleFriendRevoke)))

	// Market：公开的 agent 目录，任何登录用户都能浏览。
	// 只列 kind=normal + status=active，virtual-user / draining / inactive 不出现。
	mux.Handle("GET /market/agents", middleware.TrustGateway(http.HandlerFunc(h.handleMarketList)))

	// Market publications：用户主动发布的 agent 模板，可被别人 fork。
	mux.Handle("POST /publications", middleware.TrustGateway(http.HandlerFunc(h.handlePublicationCreate)))
	mux.Handle("GET /publications", middleware.TrustGateway(http.HandlerFunc(h.handlePublicationList)))
	mux.Handle("GET /publications/{id}", middleware.TrustGateway(http.HandlerFunc(h.handlePublicationGet)))
	mux.Handle("POST /publications/{id}/fork", middleware.TrustGateway(http.HandlerFunc(h.handlePublicationFork)))
	mux.Handle("DELETE /publications/{id}", middleware.TrustGateway(http.HandlerFunc(h.handlePublicationDelete)))
	mux.Handle("GET /users/me/subscriptions", middleware.TrustGateway(http.HandlerFunc(h.handleListMySubscriptions)))

	// 群组管理。
	mux.Handle("POST /groups", middleware.TrustGateway(http.HandlerFunc(h.handleCreateGroup)))
	mux.Handle("GET /groups", middleware.TrustGateway(http.HandlerFunc(h.handleListMyGroups)))
	mux.Handle("POST /groups/{group_id}/members", middleware.TrustGateway(http.HandlerFunc(h.handleAddGroupMember)))
	mux.Handle("DELETE /groups/{group_id}/members/{agent_id}", middleware.TrustGateway(http.HandlerFunc(h.handleRemoveGroupMember)))
	mux.Handle("GET /groups/{group_id}/members", middleware.TrustGateway(http.HandlerFunc(h.handleListGroupMembers)))
	mux.Handle("GET /groups/{group_id}/roster", middleware.TrustGateway(http.HandlerFunc(h.handleGetRoster)))

	// Task：用户通过前端给 agent 下令 / 查看任务。
	mux.Handle("POST /tasks", middleware.TrustGateway(http.HandlerFunc(h.handleAdminSubmitTask)))
	mux.Handle("POST /tasks/{task_id}/messages", middleware.TrustGateway(http.HandlerFunc(h.handleAdminAppendMessage)))
	mux.Handle("GET /tasks/{task_id}", middleware.TrustGateway(http.HandlerFunc(h.handleAdminGetTask)))
	mux.Handle("DELETE /tasks/{task_id}", middleware.TrustGateway(http.HandlerFunc(h.handleAdminDeleteTask)))
	mux.Handle("GET /tasks", middleware.TrustGateway(http.HandlerFunc(h.handleAdminListTasks)))
	mux.Handle("GET /tasks/context/{context_id}/timeline", middleware.TrustGateway(http.HandlerFunc(h.handleAdminGetTimeline)))
	mux.Handle("DELETE /tasks/cleanup", middleware.TrustGateway(http.HandlerFunc(h.handleAdminCleanupTasks)))

	// WebSocket：前端实时订阅 agent 交流事件。
	mux.Handle("GET /ws/feed", middleware.TrustGateway(http.HandlerFunc(h.handleWSFeed)))
	return mux
}

type registerReq struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type loginReq struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type tokenResp struct {
	Token              string `json:"token"`
	UID                int64  `json:"uid"`
	Username           string `json:"username"`
	VirtualUserAgentID string `json:"virtual_user_agent_id"`
}

type meResp struct {
	UID                int64  `json:"uid"`
	Username           string `json:"username"`
	VirtualUserAgentID string `json:"virtual_user_agent_id"`
}

func (h *Handler) handleRegister(w http.ResponseWriter, r *http.Request) {
	var req registerReq
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, httpx.CodeBadRequest, "invalid request body")
		return
	}
	u, err := h.users.Register(r.Context(), req.Username, req.Password)
	if err != nil {
		h.mapUserError(w, err)
		return
	}
	tok, err := h.signer.IssueUser(u.ID)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, httpx.CodeInternal, "issue token failed")
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, tokenResp{
		Token:              tok,
		UID:                u.ID,
		Username:           u.Username,
		VirtualUserAgentID: u.VirtualUserAgentID,
	})
}

func (h *Handler) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req loginReq
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, httpx.CodeBadRequest, "invalid request body")
		return
	}
	u, err := h.users.Login(r.Context(), req.Username, req.Password)
	if err != nil {
		h.mapUserError(w, err)
		return
	}
	tok, err := h.signer.IssueUser(u.ID)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, httpx.CodeInternal, "issue token failed")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, tokenResp{
		Token:              tok,
		UID:                u.ID,
		Username:           u.Username,
		VirtualUserAgentID: u.VirtualUserAgentID,
	})
}

func (h *Handler) handleMe(w http.ResponseWriter, r *http.Request) {
	uid, ok := middleware.UIDFromContext(r.Context())
	if !ok {
		httpx.WriteError(w, http.StatusUnauthorized, httpx.CodeLoginFail, "missing auth")
		return
	}
	u, err := h.users.GetByID(r.Context(), uid)
	if err != nil {
		h.mapUserError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, meResp{
		UID:                u.ID,
		Username:           u.Username,
		VirtualUserAgentID: u.VirtualUserAgentID,
	})
}

// mapUserError 把域错误翻译成 HTTP。集中处理让 handler 保持精简，且错误
// 码保持一致。
func (h *Handler) mapUserError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, user.ErrUsernameTaken):
		httpx.WriteError(w, http.StatusConflict, httpx.CodeConflict, "username already taken")
	case errors.Is(err, user.ErrInvalidPassword):
		httpx.WriteError(w, http.StatusUnauthorized, httpx.CodeLoginFail, "invalid credentials")
	case errors.Is(err, user.ErrUserNotFound):
		httpx.WriteError(w, http.StatusNotFound, httpx.CodeNotFound, "user not found")
	case errors.Is(err, user.ErrInvalidUsername):
		httpx.WriteError(w, http.StatusBadRequest, httpx.CodeBadRequest, err.Error())
	case strings.HasPrefix(err.Error(), "user: password"):
		httpx.WriteError(w, http.StatusBadRequest, httpx.CodeBadRequest, err.Error())
	default:
		httpx.WriteError(w, http.StatusInternalServerError, httpx.CodeInternal, "internal error")
	}
}

// ─── Agent 管理 ───────────────────────────────────────────────────────────

type createAgentReq struct {
	AgentID      string          `json:"agent_id"`
	Name         string          `json:"name"`
	Description  string          `json:"description,omitempty"`
	URL          string          `json:"url,omitempty"`
	Version      string          `json:"version,omitempty"`
	SystemPrompt  string          `json:"system_prompt,omitempty"`
	WorkspacePath string          `json:"workspace_path,omitempty"`
	AgentCard     json.RawMessage `json:"agent_card,omitempty"`
	Skills        []skill.Input   `json:"skills,omitempty"`
}

type agentResp struct {
	AgentID       string          `json:"agent_id"`
	Name          string          `json:"name"`
	Description   string          `json:"description,omitempty"`
	URL           string          `json:"url,omitempty"`
	Version       string          `json:"version,omitempty"`
	Kind          string          `json:"kind"`
	Status        string          `json:"status"`
	SystemPrompt  string          `json:"system_prompt,omitempty"`
	WorkspacePath string          `json:"workspace_path,omitempty"`
	AgentCard     json.RawMessage `json:"agent_card,omitempty"`
}

type createAgentResp struct {
	Agent agentResp `json:"agent"`
}

func toAgentResp(a *agent.Agent) agentResp {
	return agentResp{
		AgentID:       a.AgentID,
		Name:          a.Name,
		Description:   a.Description,
		URL:           a.URL,
		Version:       a.Version,
		Kind:          string(a.Kind),
		Status:        string(a.Status),
		SystemPrompt:  a.SystemPrompt,
		WorkspacePath: a.WorkspacePath,
		AgentCard:     json.RawMessage(a.AgentCardJSON),
	}
}

// handleCreateAgent 创建（或更新）一个调用者名下的 agent。
//
// 不再直接签 agent JWT —— agent 身份的短期凭证由
// POST /v1/mesh/auth/token 用 API Key 换取。这样就有了统一的"长期凭证 + 可吊销"
// 链路（详见 ADR 007）。
//
// agent_id 命名空间：
//   用户在 UI 输入的是 slug（"bot"），Gateway 自动拼成 "{slug}@{username}" 存 DB。
//   兼容向后：如果 caller 直接传完整带 @ 的 ID，校验通过后直接用（迁移路径 / 程序化调用）。
func (h *Handler) handleCreateAgent(w http.ResponseWriter, r *http.Request) {
	uid, _ := middleware.UIDFromContext(r.Context())
	var req createAgentReq
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, httpx.CodeBadRequest, "invalid request body")
		return
	}

	// 命名空间拼接：拿 caller 的 username
	u, err := h.users.GetByID(r.Context(), uid)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, httpx.CodeInternal, "lookup user failed")
		return
	}
	finalAgentID := req.AgentID
	if !strings.Contains(req.AgentID, "@") {
		// 用户输入的是 slug，先校验 slug 格式，再拼接
		if err := agent.ValidateSlug(req.AgentID); err != nil {
			httpx.WriteError(w, http.StatusBadRequest, httpx.CodeBadRequest, err.Error())
			return
		}
		finalAgentID = agent.ComposeAgentID(req.AgentID, u.Username)
	}
	a, err := h.agents.Register(r.Context(), agent.RegisterInput{
		AgentID:       finalAgentID,
		OwnerUID:      uid,
		Name:          req.Name,
		Description:   req.Description,
		URL:           req.URL,
		Version:       req.Version,
		SystemPrompt:  req.SystemPrompt,
		WorkspacePath: req.WorkspacePath,
		AgentCard:     req.AgentCard,
		Skills:        req.Skills,
	})
	if err != nil {
		h.mapAgentError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, createAgentResp{Agent: toAgentResp(a)})
}

func (h *Handler) handleListMyAgents(w http.ResponseWriter, r *http.Request) {
	uid, _ := middleware.UIDFromContext(r.Context())
	agents, err := h.agents.ListByOwner(r.Context(), uid)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, httpx.CodeInternal, err.Error())
		return
	}
	out := make([]agentResp, 0, len(agents))
	for _, a := range agents {
		out = append(out, toAgentResp(a))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"agents": out})
}

func (h *Handler) handleGetAgent(w http.ResponseWriter, r *http.Request) {
	uid, _ := middleware.UIDFromContext(r.Context())
	id := r.PathValue("agent_id")
	a, err := h.agents.Get(r.Context(), id)
	if err != nil {
		h.mapAgentError(w, err)
		return
	}
	// admin 接口只有 owner 能看。公开目录浏览走另一个端点（Week 2）。
	if a.OwnerUID != uid {
		httpx.WriteError(w, http.StatusForbidden, httpx.CodeNotOwner, "not the owner")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toAgentResp(a))
}

type skillResp struct {
	SkillID     string   `json:"skill_id"`
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	InputModes  []string `json:"input_modes,omitempty"`
	OutputModes []string `json:"output_modes,omitempty"`
}

func (h *Handler) handleListSkills(w http.ResponseWriter, r *http.Request) {
	uid, _ := middleware.UIDFromContext(r.Context())
	id := r.PathValue("agent_id")
	// Ownership 校验：admin 路由只有 agent owner 能看。
	a, err := h.agents.Get(r.Context(), id)
	if err != nil {
		h.mapAgentError(w, err)
		return
	}
	if a.OwnerUID != uid {
		httpx.WriteError(w, http.StatusForbidden, httpx.CodeNotOwner, "not the owner")
		return
	}
	if h.skills == nil {
		httpx.WriteError(w, http.StatusNotImplemented, httpx.CodeNotImpl, "skill domain not wired")
		return
	}
	skills, err := h.skills.ListByAgentID(r.Context(), a.AgentID)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, httpx.CodeInternal, err.Error())
		return
	}
	out := make([]skillResp, 0, len(skills))
	for _, s := range skills {
		out = append(out, skillResp{
			SkillID:     s.SkillID,
			Name:        s.Name,
			Description: s.Description,
			Tags:        s.Tags,
			InputModes:  s.InputModes,
			OutputModes: s.OutputModes,
		})
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"skills": out})
}

func (h *Handler) handleDrainAgent(w http.ResponseWriter, r *http.Request) {
	uid, _ := middleware.UIDFromContext(r.Context())
	id := r.PathValue("agent_id")
	if err := h.agents.Drain(r.Context(), id, uid); err != nil {
		h.mapAgentError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"status": "draining"})
}

func (h *Handler) handleDeleteAgent(w http.ResponseWriter, r *http.Request) {
	uid, _ := middleware.UIDFromContext(r.Context())
	id := r.PathValue("agent_id")
	if err := h.agents.Deregister(r.Context(), id, uid); err != nil {
		h.mapAgentError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) mapAgentError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, agent.ErrAgentNotFound):
		httpx.WriteError(w, http.StatusNotFound, httpx.CodeNotFound, "agent not found")
	case errors.Is(err, agent.ErrAgentIDExists):
		httpx.WriteError(w, http.StatusConflict, httpx.CodeConflict, "agent_id already exists")
	case errors.Is(err, agent.ErrInvalidAgentID):
		httpx.WriteError(w, http.StatusBadRequest, httpx.CodeBadRequest, err.Error())
	case errors.Is(err, agent.ErrReservedVirtualName):
		httpx.WriteError(w, http.StatusBadRequest, httpx.CodeBadRequest, "virtual-user-* is reserved")
	case errors.Is(err, agent.ErrNotOwner):
		httpx.WriteError(w, http.StatusForbidden, httpx.CodeNotOwner, "not the owner")
	case errors.Is(err, agent.ErrSystemPromptTooLong):
		httpx.WriteError(w, http.StatusBadRequest, httpx.CodeBadRequest, "system_prompt exceeds 8KB")
	case errors.Is(err, skill.ErrInvalidSkillID), errors.Is(err, skill.ErrInvalidName), errors.Is(err, skill.ErrDuplicateID):
		// skill 校验失败是调用方的问题，400 比通用 500 更直白。
		httpx.WriteError(w, http.StatusBadRequest, httpx.CodeBadRequest, err.Error())
	default:
		// 开发期把原始错误透出；生产 overlay 可以再包一层隐藏细节。
		httpx.WriteError(w, http.StatusInternalServerError, httpx.CodeInternal, err.Error())
	}
}

// ─── API Key 管理 ─────────────────────────────────────────────────────────

type createAPIKeyReq struct {
	Label string `json:"label,omitempty"`
}

// apiKeyResp 是"列表 / 吊销"响应中的元数据。刻意不含明文 key。
type apiKeyResp struct {
	ID         int64   `json:"id"`
	KeyPrefix  string  `json:"key_prefix"`
	Label      string  `json:"label,omitempty"`
	CreatedAt  string  `json:"created_at"`
	LastUsedAt *string `json:"last_used_at,omitempty"`
	RevokedAt  *string `json:"revoked_at,omitempty"`
}

// createAPIKeyResp 是签发接口的响应。raw_key 字段**只在此刻返回**；
// 客户端必须立即保存，之后网关永远不再透出。
type createAPIKeyResp struct {
	Key    apiKeyResp `json:"key"`
	RawKey string     `json:"raw_key"`
}

// handleCreateAPIKey 签发一把新的 API Key。
//
// 用户自行负责把 raw_key 传给 agent（写 GAS 配置、塞 env、放 vault 等）。
// Gateway 只在响应里返回一次；DB 只存 SHA-256(raw)。
func (h *Handler) handleCreateAPIKey(w http.ResponseWriter, r *http.Request) {
	if h.keys == nil {
		httpx.WriteError(w, http.StatusNotImplemented, httpx.CodeNotImpl, "apikey domain not wired")
		return
	}
	uid, _ := middleware.UIDFromContext(r.Context())
	var req createAPIKeyReq
	// body 可省略：空 label 也合法。
	_ = httpx.DecodeJSON(r, &req)

	raw, k, err := h.keys.Issue(r.Context(), uid, req.Label)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, httpx.CodeInternal, err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, createAPIKeyResp{
		Key:    toAPIKeyResp(k),
		RawKey: raw,
	})
}

// handleListAPIKeys 列出调用方名下的 key（含已吊销的），用于 UI 展示。
// 不返回明文。
func (h *Handler) handleListAPIKeys(w http.ResponseWriter, r *http.Request) {
	if h.keys == nil {
		httpx.WriteError(w, http.StatusNotImplemented, httpx.CodeNotImpl, "apikey domain not wired")
		return
	}
	uid, _ := middleware.UIDFromContext(r.Context())
	list, err := h.keys.List(r.Context(), uid)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, httpx.CodeInternal, err.Error())
		return
	}
	out := make([]apiKeyResp, 0, len(list))
	for _, k := range list {
		out = append(out, toAPIKeyResp(k))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"keys": out})
}

// handleRevokeAPIKey 吊销一把 key。吊销后任何后续刷新 JWT 的请求都会被拒；
// 已签发的 JWT 继续活到自然过期（最多 agentTTL，默认 1h）。
func (h *Handler) handleRevokeAPIKey(w http.ResponseWriter, r *http.Request) {
	if h.keys == nil {
		httpx.WriteError(w, http.StatusNotImplemented, httpx.CodeNotImpl, "apikey domain not wired")
		return
	}
	uid, _ := middleware.UIDFromContext(r.Context())
	rawID := r.PathValue("key_id")
	id, err := strconv.ParseInt(rawID, 10, 64)
	if err != nil || id <= 0 {
		httpx.WriteError(w, http.StatusBadRequest, httpx.CodeBadRequest, "invalid key_id")
		return
	}
	if err := h.keys.Revoke(r.Context(), uid, id); err != nil {
		if errors.Is(err, apikey.ErrKeyNotFound) {
			httpx.WriteError(w, http.StatusNotFound, httpx.CodeNotFound, "api key not found")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, httpx.CodeInternal, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// toAPIKeyResp 把 domain 结构体映射到响应形。nil 指针转成 *string nil，
// 让 JSON 输出自然保留 omitempty。
func toAPIKeyResp(k *apikey.Key) apiKeyResp {
	out := apiKeyResp{
		ID:        k.ID,
		KeyPrefix: k.KeyPrefix,
		Label:     k.Label,
		CreatedAt: k.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
	}
	if k.LastUsedAt != nil {
		s := k.LastUsedAt.UTC().Format("2006-01-02T15:04:05Z")
		out.LastUsedAt = &s
	}
	if k.RevokedAt != nil {
		s := k.RevokedAt.UTC().Format("2006-01-02T15:04:05Z")
		out.RevokedAt = &s
	}
	return out
}

// ─── Friendship ──────────────────────────────────────────────────────────

type friendRequestReq struct {
	FromAgentID string `json:"from_agent_id"`
	ToAgentID   string `json:"to_agent_id"`
	Reason      string `json:"reason,omitempty"`
}

// friendResp 是 friendship 对外展示形。to_agent_id / from_agent_id 原样返回。
type friendResp struct {
	ID          int64  `json:"id"`
	FromAgentID string `json:"from_agent_id"`
	ToAgentID   string `json:"to_agent_id"`
	Status      string `json:"status"`
	Reason      string `json:"reason,omitempty"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

func toFriendResp(f *friendship.Friendship) friendResp {
	return friendResp{
		ID:          f.ID,
		FromAgentID: f.FromAgentID,
		ToAgentID:   f.ToAgentID,
		Status:      string(f.Status),
		Reason:      f.Reason,
		CreatedAt:   f.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
		UpdatedAt:   f.UpdatedAt.UTC().Format("2006-01-02T15:04:05Z"),
	}
}

// handleFriendRequest 发起好友请求。调用方必须是 from_agent_id 的 owner。
func (h *Handler) handleFriendRequest(w http.ResponseWriter, r *http.Request) {
	if h.friends == nil {
		httpx.WriteError(w, http.StatusNotImplemented, httpx.CodeNotImpl, "friendship domain not wired")
		return
	}
	uid, _ := middleware.UIDFromContext(r.Context())
	var req friendRequestReq
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, httpx.CodeBadRequest, "invalid request body")
		return
	}
	f, err := h.friends.Request(r.Context(), uid, req.FromAgentID, req.ToAgentID, req.Reason)
	if err != nil {
		h.mapFriendError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, toFriendResp(f))
}

func (h *Handler) handleListFriends(w http.ResponseWriter, r *http.Request) {
	if h.friends == nil {
		httpx.WriteError(w, http.StatusNotImplemented, httpx.CodeNotImpl, "friendship domain not wired")
		return
	}
	uid, _ := middleware.UIDFromContext(r.Context())
	agentID := r.PathValue("agent_id")
	status := friendship.Status(r.URL.Query().Get("status"))

	list, err := h.friends.ListFriends(r.Context(), uid, agentID, status)
	if err != nil {
		h.mapFriendError(w, err)
		return
	}
	out := make([]friendResp, 0, len(list))
	for _, f := range list {
		out = append(out, toFriendResp(f))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"friends": out})
}

func (h *Handler) handleListIncoming(w http.ResponseWriter, r *http.Request) {
	if h.friends == nil {
		httpx.WriteError(w, http.StatusNotImplemented, httpx.CodeNotImpl, "friendship domain not wired")
		return
	}
	uid, _ := middleware.UIDFromContext(r.Context())
	agentID := r.PathValue("agent_id")

	list, err := h.friends.ListIncomingPending(r.Context(), uid, agentID)
	if err != nil {
		h.mapFriendError(w, err)
		return
	}
	out := make([]friendResp, 0, len(list))
	for _, f := range list {
		out = append(out, toFriendResp(f))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"incoming": out})
}

func (h *Handler) handleFriendAccept(w http.ResponseWriter, r *http.Request) {
	h.runFriendTransition(w, r, func(ctx context.Context, uid, id int64) (*friendship.Friendship, error) {
		return h.friends.Accept(ctx, uid, id)
	})
}

func (h *Handler) handleFriendReject(w http.ResponseWriter, r *http.Request) {
	h.runFriendTransition(w, r, func(ctx context.Context, uid, id int64) (*friendship.Friendship, error) {
		return h.friends.Reject(ctx, uid, id)
	})
}

func (h *Handler) handleFriendRevoke(w http.ResponseWriter, r *http.Request) {
	h.runFriendTransition(w, r, func(ctx context.Context, uid, id int64) (*friendship.Friendship, error) {
		return h.friends.Revoke(ctx, uid, id)
	})
}

// runFriendTransition 抽取 accept/reject/revoke 的公共 handler 壳：
// 同样的 path param 解析、同样的错误映射、同样的响应形。
func (h *Handler) runFriendTransition(
	w http.ResponseWriter, r *http.Request,
	op func(ctx context.Context, uid, id int64) (*friendship.Friendship, error),
) {
	if h.friends == nil {
		httpx.WriteError(w, http.StatusNotImplemented, httpx.CodeNotImpl, "friendship domain not wired")
		return
	}
	uid, _ := middleware.UIDFromContext(r.Context())
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		httpx.WriteError(w, http.StatusBadRequest, httpx.CodeBadRequest, "invalid friendship id")
		return
	}
	f, err := op(r.Context(), uid, id)
	if err != nil {
		h.mapFriendError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toFriendResp(f))
}

// mapFriendError 把 friendship 域错误翻译成 HTTP。
func (h *Handler) mapFriendError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, friendship.ErrNotFound):
		httpx.WriteError(w, http.StatusNotFound, httpx.CodeNotFound, "friendship not found")
	case errors.Is(err, friendship.ErrNotOwner):
		httpx.WriteError(w, http.StatusForbidden, httpx.CodeNotOwner, "not the agent owner")
	case errors.Is(err, friendship.ErrInvalidAgent):
		httpx.WriteError(w, http.StatusBadRequest, httpx.CodeBadRequest, err.Error())
	case errors.Is(err, friendship.ErrSelfFriend):
		httpx.WriteError(w, http.StatusBadRequest, httpx.CodeBadRequest, err.Error())
	case errors.Is(err, friendship.ErrVirtualUserPeer):
		// virtual-user 不参与显式 friendship；约束，不是权限问题，因此 400。
		httpx.WriteError(w, http.StatusBadRequest, httpx.CodeBadRequest, err.Error())
	case errors.Is(err, friendship.ErrAlreadyPending),
		errors.Is(err, friendship.ErrAlreadyAccepted):
		httpx.WriteError(w, http.StatusConflict, httpx.CodeConflict, err.Error())
	case errors.Is(err, friendship.ErrInvalidTransition):
		// 状态机拒绝：当前状态不允许这个操作。409 比 400 更准确——资源存在但冲突。
		httpx.WriteError(w, http.StatusConflict, httpx.CodeConflict, err.Error())
	default:
		httpx.WriteError(w, http.StatusInternalServerError, httpx.CodeInternal, err.Error())
	}
}

// ─── Market ──────────────────────────────────────────────────────────────

// marketSkillResp 是 market 列表里 agent 的 skill 简表。
// 不返回时间戳等元信息，只给 UI 做"能不能协作"的判断足够。
type marketSkillResp struct {
	SkillID     string   `json:"skill_id"`
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Tags        []string `json:"tags,omitempty"`
}

// marketAgentResp 是 market 列表里的 agent 条目。
//
// **不含敏感字段**：owner_uid / url / version / last_heartbeat_at 全部不对外暴露。
// url 尤其敏感 —— 它是 agent 的入站地址，泄露出去会给 agent 直接被打。
type marketAgentResp struct {
	AgentID     string            `json:"agent_id"`
	Name        string            `json:"name"`
	Description string            `json:"description,omitempty"`
	Skills      []marketSkillResp `json:"skills"`
}

// defaultMarketLimit / maxMarketLimit 控制分页大小。上限防止客户端拉全表。
const (
	defaultMarketLimit = 20
	maxMarketLimit     = 100
)

// handleMarketList 返回 market 目录。
//
//	GET /v1/admin/market/agents?search=reviewer&limit=20&offset=0
//
// 查询流程：
//  1. agent.Service.ListMarket：过滤 kind=normal + status=active + LIKE
//  2. skill.Repo.ListByAgentIDs：一次批量拉回所有 agent 的 skills
//  3. 组装响应，skills 按 agent_id 映射
func (h *Handler) handleMarketList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	search := strings.TrimSpace(q.Get("search"))
	limit := parsePositiveInt(q.Get("limit"), defaultMarketLimit)
	if limit > maxMarketLimit {
		limit = maxMarketLimit
	}
	offset := parseNonNegativeInt(q.Get("offset"), 0)

	agents, err := h.agents.ListMarket(r.Context(), search, limit, offset)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, httpx.CodeInternal, err.Error())
		return
	}

	// skills 批量拉，避免 N+1。
	ids := make([]string, 0, len(agents))
	for _, a := range agents {
		ids = append(ids, a.AgentID)
	}
	var skillMap map[string][]*skill.Skill
	if h.skills != nil && len(ids) > 0 {
		skillMap, err = h.skills.ListByAgentIDs(r.Context(), ids)
		if err != nil {
			// skills 拉取失败降级成空列表 —— market 主体（agent 列表）仍应可展示。
			skillMap = map[string][]*skill.Skill{}
		}
	} else {
		skillMap = map[string][]*skill.Skill{}
	}

	out := make([]marketAgentResp, 0, len(agents))
	for _, a := range agents {
		skills := make([]marketSkillResp, 0, len(skillMap[a.AgentID]))
		for _, s := range skillMap[a.AgentID] {
			skills = append(skills, marketSkillResp{
				SkillID:     s.SkillID,
				Name:        s.Name,
				Description: s.Description,
				Tags:        s.Tags,
			})
		}
		out = append(out, marketAgentResp{
			AgentID:     a.AgentID,
			Name:        a.Name,
			Description: a.Description,
			Skills:      skills,
		})
	}

	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"agents": out,
		"limit":  limit,
		"offset": offset,
	})
}

// parsePositiveInt 解析 >0 的整数；非法 / 零 / 负值 → fallback。
func parsePositiveInt(s string, fallback int) int {
	if s == "" {
		return fallback
	}
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return fallback
	}
	return n
}

// parseNonNegativeInt 解析 >=0 的整数；非法 / 负值 → fallback。
func parseNonNegativeInt(s string, fallback int) int {
	if s == "" {
		return fallback
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 {
		return fallback
	}
	return n
}

// 保留 context import，后续 handler 会用到。
var _ = context.Canceled

// ─── Publication handlers (Market) ───────────────────────────────────────

type publicationResp struct {
	ID                   int64    `json:"id"`
	PublisherUID         int64    `json:"publisher_uid"`
	SourceAgentID        string   `json:"source_agent_id"`
	Title                string   `json:"title"`
	Summary              string   `json:"summary"`
	SystemPromptTemplate string   `json:"system_prompt_template,omitempty"`
	Category             string   `json:"category,omitempty"`
	Tags                 []string `json:"tags"`
	DownloadCount        int64    `json:"download_count"`
	CreatedAt            string   `json:"created_at"`
}

func toPublicationResp(p *publication.Publication) publicationResp {
	tags := p.Tags
	if tags == nil {
		tags = []string{}
	}
	return publicationResp{
		ID:                   p.ID,
		PublisherUID:         p.PublisherUID,
		SourceAgentID:        p.SourceAgentID,
		Title:                p.Title,
		Summary:              p.Summary,
		SystemPromptTemplate: p.SystemPromptTemplate,
		Category:             p.Category,
		Tags:                 tags,
		DownloadCount:        p.DownloadCount,
		CreatedAt:            p.CreatedAt.Format(time.RFC3339),
	}
}

type publicationCreateReq struct {
	SourceAgentID string   `json:"source_agent_id"`
	Title         string   `json:"title"`
	Summary       string   `json:"summary"`
	Category      string   `json:"category"`
	Tags          []string `json:"tags"`
}

func (h *Handler) handlePublicationCreate(w http.ResponseWriter, r *http.Request) {
	if h.publications == nil {
		httpx.WriteError(w, http.StatusNotImplemented, httpx.CodeNotImpl, "publications not wired")
		return
	}
	uid, _ := middleware.UIDFromContext(r.Context())
	var req publicationCreateReq
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, httpx.CodeBadRequest, "invalid request body")
		return
	}
	p, err := h.publications.Publish(r.Context(), publication.PublishInput{
		PublisherUID:  uid,
		SourceAgentID: req.SourceAgentID,
		Title:         req.Title,
		Summary:       req.Summary,
		Category:      req.Category,
		Tags:          req.Tags,
	})
	if err != nil {
		mapPublicationError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, toPublicationResp(p))
}

func (h *Handler) handlePublicationList(w http.ResponseWriter, r *http.Request) {
	if h.publications == nil {
		httpx.WriteError(w, http.StatusNotImplemented, httpx.CodeNotImpl, "publications not wired")
		return
	}
	q := r.URL.Query()
	f := publication.Filter{
		Category: strings.TrimSpace(q.Get("category")),
		Search:   strings.TrimSpace(q.Get("search")),
		Limit:    parsePositiveInt(q.Get("limit"), 50),
		Offset:   parseNonNegativeInt(q.Get("offset"), 0),
	}
	if q.Get("mine") == "1" {
		uid, _ := middleware.UIDFromContext(r.Context())
		f.PublisherUID = uid
	}
	list, err := h.publications.List(r.Context(), f)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, httpx.CodeInternal, err.Error())
		return
	}
	out := make([]publicationResp, 0, len(list))
	for _, p := range list {
		out = append(out, toPublicationResp(p))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"publications": out})
}

func (h *Handler) handlePublicationGet(w http.ResponseWriter, r *http.Request) {
	if h.publications == nil {
		httpx.WriteError(w, http.StatusNotImplemented, httpx.CodeNotImpl, "publications not wired")
		return
	}
	id, ok := parseIDParam(r, "id")
	if !ok {
		httpx.WriteError(w, http.StatusBadRequest, httpx.CodeBadRequest, "invalid id")
		return
	}
	p, err := h.publications.Get(r.Context(), id)
	if err != nil {
		mapPublicationError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toPublicationResp(p))
}

type publicationForkReq struct {
	NewAgentID   string `json:"new_agent_id"`
	NewAgentName string `json:"new_agent_name"`
}

type publicationForkResp struct {
	Subscription map[string]any `json:"subscription"`
	Agent        map[string]any `json:"agent"`
}

func (h *Handler) handlePublicationFork(w http.ResponseWriter, r *http.Request) {
	if h.publications == nil {
		httpx.WriteError(w, http.StatusNotImplemented, httpx.CodeNotImpl, "publications not wired")
		return
	}
	uid, _ := middleware.UIDFromContext(r.Context())
	id, ok := parseIDParam(r, "id")
	if !ok {
		httpx.WriteError(w, http.StatusBadRequest, httpx.CodeBadRequest, "invalid id")
		return
	}
	var req publicationForkReq
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, httpx.CodeBadRequest, "invalid request body")
		return
	}
	if strings.TrimSpace(req.NewAgentID) == "" {
		httpx.WriteError(w, http.StatusBadRequest, httpx.CodeBadRequest, "new_agent_id required")
		return
	}
	sub, ag, err := h.publications.Fork(r.Context(), publication.ForkInput{
		PublicationID: id,
		ForkerUID:     uid,
		NewAgentID:    req.NewAgentID,
		NewAgentName:  req.NewAgentName,
	})
	if err != nil {
		mapPublicationError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, publicationForkResp{
		Subscription: map[string]any{
			"id":              sub.ID,
			"publication_id":  sub.PublicationID,
			"forked_agent_id": sub.ForkedAgentID,
			"created_at":      sub.CreatedAt.Format(time.RFC3339),
		},
		Agent: map[string]any{
			"agent_id":    ag.AgentID,
			"name":        ag.Name,
			"description": ag.Description,
			"kind":        string(ag.Kind),
			"status":      string(ag.Status),
		},
	})
}

func (h *Handler) handlePublicationDelete(w http.ResponseWriter, r *http.Request) {
	if h.publications == nil {
		httpx.WriteError(w, http.StatusNotImplemented, httpx.CodeNotImpl, "publications not wired")
		return
	}
	uid, _ := middleware.UIDFromContext(r.Context())
	id, ok := parseIDParam(r, "id")
	if !ok {
		httpx.WriteError(w, http.StatusBadRequest, httpx.CodeBadRequest, "invalid id")
		return
	}
	if err := h.publications.Delete(r.Context(), id, uid); err != nil {
		mapPublicationError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) handleListMySubscriptions(w http.ResponseWriter, r *http.Request) {
	if h.publications == nil {
		httpx.WriteError(w, http.StatusNotImplemented, httpx.CodeNotImpl, "publications not wired")
		return
	}
	uid, _ := middleware.UIDFromContext(r.Context())
	list, err := h.publications.ListSubscriptions(r.Context(), uid)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, httpx.CodeInternal, err.Error())
		return
	}
	out := make([]map[string]any, 0, len(list))
	for _, s := range list {
		out = append(out, map[string]any{
			"id":              s.ID,
			"publication_id":  s.PublicationID,
			"forked_agent_id": s.ForkedAgentID,
			"created_at":      s.CreatedAt.Format(time.RFC3339),
		})
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"subscriptions": out})
}

// mapPublicationError 把 domain error 映射到 HTTP 状态。
func mapPublicationError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, publication.ErrPublicationNotFound):
		httpx.WriteError(w, http.StatusNotFound, httpx.CodeNotFound, "publication not found")
	case errors.Is(err, publication.ErrTitleRequired):
		httpx.WriteError(w, http.StatusBadRequest, httpx.CodeBadRequest, "title required")
	case errors.Is(err, publication.ErrTitleTooLong):
		httpx.WriteError(w, http.StatusBadRequest, httpx.CodeBadRequest, "title too long")
	case errors.Is(err, publication.ErrSummaryTooLong):
		httpx.WriteError(w, http.StatusBadRequest, httpx.CodeBadRequest, "summary too long")
	case errors.Is(err, publication.ErrTagsTooLong):
		httpx.WriteError(w, http.StatusBadRequest, httpx.CodeBadRequest, "tags too long")
	case errors.Is(err, publication.ErrAlreadySubscribed):
		httpx.WriteError(w, http.StatusConflict, httpx.CodeBadRequest, "already subscribed")
	case errors.Is(err, publication.ErrCannotForkOwn):
		httpx.WriteError(w, http.StatusBadRequest, httpx.CodeBadRequest, "cannot fork your own publication")
	case errors.Is(err, agent.ErrNotOwner):
		httpx.WriteError(w, http.StatusForbidden, httpx.CodeForbidden, "not the owner of source agent")
	case errors.Is(err, agent.ErrAgentIDExists):
		httpx.WriteError(w, http.StatusConflict, httpx.CodeBadRequest, "new_agent_id already exists")
	case errors.Is(err, agent.ErrAgentNotFound):
		httpx.WriteError(w, http.StatusNotFound, httpx.CodeNotFound, "source agent not found")
	default:
		httpx.WriteError(w, http.StatusInternalServerError, httpx.CodeInternal, err.Error())
	}
}

func parseIDParam(r *http.Request, name string) (int64, bool) {
	raw := r.PathValue(name)
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return 0, false
	}
	return id, true
}
