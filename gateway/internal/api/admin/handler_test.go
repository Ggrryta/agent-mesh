// Package admin 的 handler 层集成测试。
//
// 不用 mock —— 直接用内存 repo 组装出真实的 domain service，以 *Handler 为
// 被测对象调用 http.Handler。这样既测了 handler 又顺带验证了 domain 的
// wiring；同时避免对 live DB 的依赖（pure unit test 模式）。
//
// 内存实现：
//   - user.Repo：用 map 支持 CreateWithVirtualAgent / GetByUsername / GetByID
//   - agent.Repo：用 map 支持 Upsert / GetByAgentID / List 等
//   - skill.Repo：用 map 支持 ReplaceByAgentID / ListByAgentID / ListByAgentIDs
//   - apikey.Repo：用 map 支持 Insert / FindByHash / Revoke / ListByOwner
//   - friendship.Repo：用 map 支持全部方法
//
// 测试只覆盖 handler 能看到的错误路径 + 主 happy path；更深层的 domain 语义
// （如 friendship 的状态机分支）由对应 domain 的 service_test 覆盖。
package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Ggrryta/agent-mesh/gateway/internal/domain/agent"
	"github.com/Ggrryta/agent-mesh/gateway/internal/domain/apikey"
	"github.com/Ggrryta/agent-mesh/gateway/internal/domain/friendship"
	"github.com/Ggrryta/agent-mesh/gateway/internal/domain/skill"
	"github.com/Ggrryta/agent-mesh/gateway/internal/domain/user"
	"github.com/Ggrryta/agent-mesh/gateway/internal/middleware"
	"github.com/Ggrryta/agent-mesh/gateway/pkg/auth"
)

// ── 测试 harness ──────────────────────────────────────────────────────

type testHarness struct {
	t       *testing.T
	handler http.Handler
	signer  *auth.Signer
	agents  *agent.Service
	users   *user.Service
	keys    *apikey.Service
	friends *friendship.Service
}

func newTestHarness(t *testing.T) *testHarness {
	t.Helper()

	signer, err := auth.NewSigner("test_secret_for_admin_handler_____", time.Hour, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	userRepo := newMemUserRepo()
	userSvc := user.NewService(userRepo)

	agentRepo := newMemAgentRepo(userRepo)
	agentCache := agent.NewCache()
	skillRepo := newMemSkillRepo()
	agentSvc := agent.NewService(agentRepo, agentCache).WithSkills(skill.NewAdapter(skillRepo))

	apikeyRepo := newMemAPIKeyRepo()
	apikeySvc := apikey.NewService(apikeyRepo, nil)

	friendRepo := newMemFriendRepo()
	// AgentLookup：用 agentRepo 查 ownerUID / kind。
	friendSvc := friendship.NewService(friendRepo, &agentLookupFromRepo{repo: agentRepo})

	h := New(userSvc, agentSvc, skillRepo, apikeySvc, friendSvc, signer)
	return &testHarness{
		t:       t,
		handler: h.Mux(),
		signer:  signer,
		agents:  agentSvc,
		users:   userSvc,
		keys:    apikeySvc,
		friends: friendSvc,
	}
}

// do 发起一个请求并返回 response recorder。
// authToken 非空时解析 JWT 并注入 X-Mesh-* header（模拟 Gateway 层已完成认证）。
func (h *testHarness) do(method, path, authToken string, body any) *httptest.ResponseRecorder {
	h.t.Helper()
	var r *http.Request
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			h.t.Fatal(err)
		}
		r = httptest.NewRequest(method, path, bytes.NewReader(b))
		r.Header.Set("Content-Type", "application/json")
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	if authToken != "" {
		claims, err := h.signer.Verify(authToken)
		if err == nil {
			r.Header.Set(middleware.HeaderMeshUID, strconv.FormatInt(claims.UID, 10))
			r.Header.Set(middleware.HeaderMeshTokenKind, string(claims.Kind))
			if claims.AgentID != "" {
				r.Header.Set(middleware.HeaderMeshAgentID, claims.AgentID)
			}
			if claims.KeyID != 0 {
				r.Header.Set(middleware.HeaderMeshKeyID, strconv.FormatInt(claims.KeyID, 10))
			}
		}
	}
	rr := httptest.NewRecorder()
	h.handler.ServeHTTP(rr, r)
	return rr
}

// register 做"注册 → 拿 user token"，供后续测试引用。
func (h *testHarness) register(username, password string) (uid int64, token string) {
	h.t.Helper()
	rr := h.do(http.MethodPost, "/auth/register", "", map[string]string{
		"username": username, "password": password,
	})
	if rr.Code != http.StatusCreated {
		h.t.Fatalf("register %s: want 201, got %d body=%s", username, rr.Code, rr.Body.String())
	}
	var resp struct {
		Token string `json:"token"`
		UID   int64  `json:"uid"`
	}
	mustDecode(h.t, rr, &resp)
	return resp.UID, resp.Token
}

// createAgent 以某 user 身份建一个 normal agent。
//
// 注意：handleCreateAgent 会自动给 agent_id 拼上 owner username 命名空间
// （"alice-dev" → "alice-dev@<owner_username>"）。本 helper 返回 DB 实存的完整 ID。
func (h *testHarness) createAgent(token, agentID, name string) string {
	h.t.Helper()
	rr := h.do(http.MethodPost, "/users/me/agents", token, map[string]string{
		"agent_id": agentID, "name": name, "url": "http://" + agentID + ":0",
	})
	if rr.Code != http.StatusCreated {
		h.t.Fatalf("create agent %s: want 201, got %d body=%s", agentID, rr.Code, rr.Body.String())
	}
	// 从 response 拿 handler 实际写下的 agent_id（带命名空间）
	var resp struct {
		Agent struct {
			AgentID string `json:"agent_id"`
		} `json:"agent"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		h.t.Fatalf("parse create response: %v", err)
	}
	finalID := resp.Agent.AgentID
	// 模拟 GAS daemon 首次心跳，让 agent 从 inactive → active。
	// 否则它不会出现在 market 里（market 只列 active agent）。
	a, err := h.agents.Get(context.Background(), finalID)
	if err != nil {
		h.t.Fatalf("get agent %s: %v", finalID, err)
	}
	if _, err := h.agents.Heartbeat(context.Background(), finalID, a.OwnerUID); err != nil {
		h.t.Fatalf("activate via heartbeat %s: %v", finalID, err)
	}
	return finalID
}

func mustDecode(t *testing.T, rr *httptest.ResponseRecorder, v any) {
	t.Helper()
	if err := json.NewDecoder(rr.Body).Decode(v); err != nil {
		t.Fatalf("decode: %v body=%s", err, rr.Body.String())
	}
}

func errCode(t *testing.T, rr *httptest.ResponseRecorder) int {
	t.Helper()
	var body struct {
		Code int `json:"code"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &body)
	return body.Code
}

// ── Auth / User ────────────────────────────────────────────────────────

func TestHandler_Register_Login_Me(t *testing.T) {
	h := newTestHarness(t)

	// Register 成功
	uid, token := h.register("alice", "pw123456")
	if uid == 0 || token == "" {
		t.Fatal("expected uid + token")
	}

	// Register 重复 username → 409
	rr := h.do(http.MethodPost, "/auth/register", "", map[string]string{
		"username": "alice", "password": "pw123456",
	})
	if rr.Code != http.StatusConflict {
		t.Fatalf("dup register: want 409, got %d", rr.Code)
	}

	// Register body 格式错 → 400
	rr = h.do(http.MethodPost, "/auth/register", "", "not-an-object")
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("bad body: want 400, got %d", rr.Code)
	}

	// Login 成功
	rr = h.do(http.MethodPost, "/auth/login", "", map[string]string{
		"username": "alice", "password": "pw123456",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("login: want 200, got %d", rr.Code)
	}

	// Login 错密码 → 401
	rr = h.do(http.MethodPost, "/auth/login", "", map[string]string{
		"username": "alice", "password": "wrong",
	})
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("wrong pw: want 401, got %d", rr.Code)
	}

	// Login 不存在的用户 → 401（防枚举，不是 404）
	rr = h.do(http.MethodPost, "/auth/login", "", map[string]string{
		"username": "ghost", "password": "whatever",
	})
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("ghost login: want 401, got %d", rr.Code)
	}

	// GetMe 无 token → 401
	rr = h.do(http.MethodGet, "/users/me", "", nil)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("no token: want 401, got %d", rr.Code)
	}

	// GetMe 有 token → 200
	rr = h.do(http.MethodGet, "/users/me", token, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("me: want 200, got %d", rr.Code)
	}
}

// ── Agent ──────────────────────────────────────────────────────────────

func TestHandler_Agent_CRUD(t *testing.T) {
	h := newTestHarness(t)
	_, t1 := h.register("alice", "pw123456")
	_, t2 := h.register("bob", "pw123456")

	// 创建成功
	aliceDev := h.createAgent(t1, "alice-dev", "Alice")
	if aliceDev != "alice-dev@alice" {
		t.Fatalf("expected namespaced id, got %s", aliceDev)
	}

	// 同 slug 不同 owner → 不再冲突（命名空间隔离），各自创建
	bobAlias := h.createAgent(t2, "alice-dev", "Bob's alice-dev")
	if bobAlias != "alice-dev@bob" {
		t.Fatalf("expected separate namespace, got %s", bobAlias)
	}

	// agent_id 格式错（slug 校验）→ 400
	rr := h.do(http.MethodPost, "/users/me/agents", t1, map[string]string{
		"agent_id": "BAD ID", "name": "x",
	})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("bad agent_id: want 400, got %d", rr.Code)
	}

	// 列表
	rr = h.do(http.MethodGet, "/users/me/agents", t1, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("list: want 200, got %d", rr.Code)
	}

	// GetByID owner → 200
	rr = h.do(http.MethodGet, "/agents/"+aliceDev, t1, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("get owner: want 200, got %d", rr.Code)
	}

	// GetByID 非 owner → 403
	rr = h.do(http.MethodGet, "/agents/"+aliceDev, t2, nil)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("get non-owner: want 403, got %d", rr.Code)
	}

	// GetByID 不存在 → 404
	rr = h.do(http.MethodGet, "/agents/ghost@nobody", t1, nil)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("get ghost: want 404, got %d", rr.Code)
	}

	// Drain 成功
	rr = h.do(http.MethodPost, "/agents/"+aliceDev+"/drain", t1, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("drain: want 200, got %d", rr.Code)
	}

	// Delete 非 owner → 403
	rr = h.do(http.MethodDelete, "/agents/"+aliceDev, t2, nil)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("delete non-owner: want 403, got %d", rr.Code)
	}

	// Delete owner → 204
	rr = h.do(http.MethodDelete, "/agents/"+aliceDev, t1, nil)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("delete owner: want 204, got %d", rr.Code)
	}
}

func TestHandler_ListSkills(t *testing.T) {
	h := newTestHarness(t)
	_, t1 := h.register("alice", "pw123456")
	aliceDev := h.createAgent(t1, "alice-dev", "Alice")

	// 未建 skill 时返回空列表（不是 404）
	rr := h.do(http.MethodGet, "/agents/"+aliceDev+"/skills", t1, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("skills: want 200, got %d body=%s", rr.Code, rr.Body.String())
	}
}

// ── API Key ────────────────────────────────────────────────────────────

func TestHandler_APIKey_CRUD(t *testing.T) {
	h := newTestHarness(t)
	_, t1 := h.register("alice", "pw123456")

	// 签发
	rr := h.do(http.MethodPost, "/users/me/api-keys", t1, map[string]string{
		"label": "ci",
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("create key: want 201, got %d", rr.Code)
	}
	var issued struct {
		Key struct {
			ID int64 `json:"id"`
		} `json:"key"`
		RawKey string `json:"raw_key"`
	}
	mustDecode(t, rr, &issued)
	if issued.Key.ID == 0 || !strings.HasPrefix(issued.RawKey, "sk-am_") {
		t.Fatalf("unexpected issue resp: %+v", issued)
	}

	// 列表
	rr = h.do(http.MethodGet, "/users/me/api-keys", t1, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("list keys: want 200, got %d", rr.Code)
	}

	// Revoke 成功
	rr = h.do(http.MethodDelete,
		fmt.Sprintf("/users/me/api-keys/%d", issued.Key.ID), t1, nil)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("revoke: want 204, got %d", rr.Code)
	}

	// Revoke 非法 id → 400
	rr = h.do(http.MethodDelete, "/users/me/api-keys/xyz", t1, nil)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("bad id: want 400, got %d", rr.Code)
	}

	// Revoke 不存在的 id → 404
	rr = h.do(http.MethodDelete, "/users/me/api-keys/99999", t1, nil)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("unknown id: want 404, got %d", rr.Code)
	}
}

// ── Friendship ─────────────────────────────────────────────────────────

func TestHandler_Friendship_FullFlow(t *testing.T) {
	h := newTestHarness(t)
	_, t1 := h.register("alice", "pw123456")
	_, t2 := h.register("bob", "pw123456")
	aliceDev := h.createAgent(t1, "alice-dev", "Alice")
	bobDev := h.createAgent(t2, "bob-dev", "Bob")

	// Request 成功
	rr := h.do(http.MethodPost, "/friends", t1, map[string]string{
		"from_agent_id": aliceDev,
		"to_agent_id":   bobDev,
		"reason":        "hi",
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("request: want 201, got %d body=%s", rr.Code, rr.Body.String())
	}
	var created struct {
		ID int64 `json:"id"`
	}
	mustDecode(t, rr, &created)

	// 重复 → 409
	rr = h.do(http.MethodPost, "/friends", t1, map[string]string{
		"from_agent_id": aliceDev,
		"to_agent_id":   bobDev,
	})
	if rr.Code != http.StatusConflict {
		t.Fatalf("dup request: want 409, got %d", rr.Code)
	}

	// alice 借 bob-dev 名义发 → 403
	rr = h.do(http.MethodPost, "/friends", t1, map[string]string{
		"from_agent_id": bobDev,
		"to_agent_id":   aliceDev,
	})
	if rr.Code != http.StatusForbidden {
		t.Fatalf("wrong owner: want 403, got %d", rr.Code)
	}

	// 拉 virtual-user → 400
	rr = h.do(http.MethodPost, "/friends", t1, map[string]string{
		"from_agent_id": aliceDev,
		"to_agent_id":   "virtual-user-2",
	})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("virtual-user: want 400, got %d", rr.Code)
	}

	// bob 看 incoming
	rr = h.do(http.MethodGet, "/agents/"+bobDev+"/friends/incoming", t2, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("incoming: want 200, got %d", rr.Code)
	}

	// alice 试图 accept → 403
	rr = h.do(http.MethodPost,
		fmt.Sprintf("/friends/%d/accept", created.ID), t1, nil)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("alice accept: want 403, got %d", rr.Code)
	}

	// bad id → 400
	rr = h.do(http.MethodPost, "/friends/not-a-number/accept", t2, nil)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("bad id: want 400, got %d", rr.Code)
	}

	// bob accept → 200
	rr = h.do(http.MethodPost,
		fmt.Sprintf("/friends/%d/accept", created.ID), t2, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("accept: want 200, got %d", rr.Code)
	}

	// accepted 后再 Request → 409
	rr = h.do(http.MethodPost, "/friends", t1, map[string]string{
		"from_agent_id": aliceDev,
		"to_agent_id":   bobDev,
	})
	if rr.Code != http.StatusConflict {
		t.Fatalf("already accepted: want 409, got %d", rr.Code)
	}

	// List alice 的朋友
	rr = h.do(http.MethodGet,
		"/agents/"+aliceDev+"/friends?status=accepted", t1, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("list: want 200, got %d", rr.Code)
	}

	// 跨 owner List → 403
	rr = h.do(http.MethodGet, "/agents/"+aliceDev+"/friends", t2, nil)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("cross-owner list: want 403, got %d", rr.Code)
	}

	// Revoke by alice → 200
	rr = h.do(http.MethodPost,
		fmt.Sprintf("/friends/%d/revoke", created.ID), t1, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("revoke: want 200, got %d", rr.Code)
	}

	// 重新 Request（覆盖回 pending）
	rr = h.do(http.MethodPost, "/friends", t1, map[string]string{
		"from_agent_id": aliceDev,
		"to_agent_id":   bobDev,
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("re-request: want 201, got %d", rr.Code)
	}

	// Reject
	rr = h.do(http.MethodPost,
		fmt.Sprintf("/friends/%d/reject", created.ID), t2, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("reject: want 200, got %d", rr.Code)
	}

	// 已 rejected 再 accept → 409 invalid_transition
	rr = h.do(http.MethodPost,
		fmt.Sprintf("/friends/%d/accept", created.ID), t2, nil)
	if rr.Code != http.StatusConflict {
		t.Fatalf("accept rejected: want 409, got %d code=%d",
			rr.Code, errCode(t, rr))
	}
}

// ── Market ─────────────────────────────────────────────────────────────

func TestHandler_Market(t *testing.T) {
	h := newTestHarness(t)
	_, t1 := h.register("alice", "pw123456")
	_, t2 := h.register("bob", "pw123456")
	reviewerID := h.createAgent(t1, "reviewer-pro", "Code Reviewer")
	h.createAgent(t2, "translator", "Translator")

	rr := h.do(http.MethodGet, "/market/agents", t1, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("market: want 200, got %d", rr.Code)
	}
	var resp struct {
		Agents []struct {
			AgentID string `json:"agent_id"`
		} `json:"agents"`
		Limit  int `json:"limit"`
		Offset int `json:"offset"`
	}
	mustDecode(t, rr, &resp)
	// 至少看到我们建的两个 normal agent；virtual-user 不该出现。
	foundReviewer := false
	for _, a := range resp.Agents {
		if a.AgentID == reviewerID {
			foundReviewer = true
		}
		if strings.HasPrefix(a.AgentID, "virtual-user-") {
			t.Fatalf("virtual-user leaked to market: %s", a.AgentID)
		}
	}
	if !foundReviewer {
		t.Fatalf("%s missing from market: %+v", reviewerID, resp.Agents)
	}
	if resp.Limit != defaultMarketLimit {
		t.Fatalf("default limit: want %d, got %d", defaultMarketLimit, resp.Limit)
	}

	// search 过滤（按 slug 前缀搜）
	rr = h.do(http.MethodGet, "/market/agents?search=review", t1, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("search: want 200, got %d", rr.Code)
	}
	mustDecode(t, rr, &resp)
	if len(resp.Agents) != 1 || resp.Agents[0].AgentID != reviewerID {
		t.Fatalf("search filter: %+v", resp.Agents)
	}

	// limit 上限截断
	rr = h.do(http.MethodGet, "/market/agents?limit=9999", t1, nil)
	mustDecode(t, rr, &resp)
	if resp.Limit != maxMarketLimit {
		t.Fatalf("limit cap: want %d, got %d", maxMarketLimit, resp.Limit)
	}

	// limit 非法值走默认
	rr = h.do(http.MethodGet, "/market/agents?limit=abc&offset=-5", t1, nil)
	mustDecode(t, rr, &resp)
	if resp.Limit != defaultMarketLimit || resp.Offset != 0 {
		t.Fatalf("bad params: limit=%d offset=%d", resp.Limit, resp.Offset)
	}
}

// ── 依赖的内存 repo 实现 ─────────────────────────────────────────────

// memUserRepo 实现 user.Repo。
type memUserRepo struct {
	mu         sync.Mutex
	byID       map[int64]*user.User
	byUsername map[string]*user.User
	nextID     int64
}

func newMemUserRepo() *memUserRepo {
	return &memUserRepo{
		byID:       map[int64]*user.User{},
		byUsername: map[string]*user.User{},
	}
}

func (r *memUserRepo) CreateWithVirtualAgent(_ context.Context, username, hash string) (*user.User, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.byUsername[username]; dup {
		return nil, user.ErrUsernameTaken
	}
	r.nextID++
	u := &user.User{
		ID:                 r.nextID,
		Username:           username,
		PasswordHash:       hash,
		VirtualUserAgentID: user.VirtualAgentIDFor(r.nextID),
	}
	r.byID[u.ID] = u
	r.byUsername[username] = u
	return u, nil
}

func (r *memUserRepo) GetByUsername(_ context.Context, name string) (*user.User, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	u, ok := r.byUsername[name]
	if !ok {
		return nil, user.ErrUserNotFound
	}
	cp := *u
	return &cp, nil
}

func (r *memUserRepo) GetByID(_ context.Context, id int64) (*user.User, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	u, ok := r.byID[id]
	if !ok {
		return nil, user.ErrUserNotFound
	}
	cp := *u
	return &cp, nil
}

// memAgentRepo 实现 agent.Repo 的必要子集，并让 virtual-user-{uid} 对应
// 已注册的 user。CreateWithVirtualAgent 的隐式 virtual-user agent 要手动
// 在这里注册 —— 通过 seedVirtualAgent 钩到 userRepo。
type memAgentRepo struct {
	mu       sync.Mutex
	byID     map[string]*agent.Agent
	userRepo *memUserRepo
}

func newMemAgentRepo(u *memUserRepo) *memAgentRepo {
	return &memAgentRepo{byID: map[string]*agent.Agent{}, userRepo: u}
}

// maybeSeedVirtual 如果给定 owner 对应的 virtual-user agent 还不在 map 里，
// 先插入一行。这模拟真实 user.Repo.CreateWithVirtualAgent 的 tx 内 insert。
func (r *memAgentRepo) maybeSeedVirtual(ownerUID int64) {
	id := user.VirtualAgentIDFor(ownerUID)
	if _, ok := r.byID[id]; ok {
		return
	}
	r.byID[id] = &agent.Agent{
		AgentID:  id,
		OwnerUID: ownerUID,
		Name:     "virtual",
		Kind:     agent.KindVirtualUser,
		Status:   agent.StatusActive,
	}
}

func (r *memAgentRepo) Create(_ context.Context, a *agent.Agent) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.byID[a.AgentID]; dup {
		return agent.ErrAgentIDExists
	}
	cp := *a
	r.byID[a.AgentID] = &cp
	return nil
}

func (r *memAgentRepo) Upsert(_ context.Context, a *agent.Agent) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.maybeSeedVirtual(a.OwnerUID)
	existing, ok := r.byID[a.AgentID]
	if !ok {
		cp := *a
		r.byID[a.AgentID] = &cp
		return nil
	}
	// 不更新 owner_uid（对齐真实 repo 行为）
	existing.Name = a.Name
	existing.Description = a.Description
	existing.URL = a.URL
	existing.Version = a.Version
	existing.Status = a.Status
	existing.AgentCardJSON = a.AgentCardJSON
	existing.LastHeartbeatAt = a.LastHeartbeatAt
	return nil
}

func (r *memAgentRepo) GetByAgentID(_ context.Context, id string) (*agent.Agent, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.maybeSeedAllVirtuals()
	a, ok := r.byID[id]
	if !ok {
		return nil, agent.ErrAgentNotFound
	}
	cp := *a
	return &cp, nil
}

func (r *memAgentRepo) UpdateStatus(_ context.Context, id string, s agent.Status) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	a, ok := r.byID[id]
	if !ok {
		return agent.ErrAgentNotFound
	}
	a.Status = s
	return nil
}

func (r *memAgentRepo) UpdateHeartbeat(_ context.Context, id string, ts time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	a, ok := r.byID[id]
	if !ok {
		return agent.ErrAgentNotFound
	}
	a.LastHeartbeatAt = &ts
	if a.Status == agent.StatusInactive {
		a.Status = agent.StatusActive
	}
	return nil
}

func (r *memAgentRepo) Delete(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.byID[id]; !ok {
		return agent.ErrAgentNotFound
	}
	delete(r.byID, id)
	return nil
}

func (r *memAgentRepo) List(_ context.Context, filter agent.Filter) ([]*agent.Agent, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.maybeSeedAllVirtuals()
	out := make([]*agent.Agent, 0)
	for _, a := range r.byID {
		if filter.OwnerUID != 0 && a.OwnerUID != filter.OwnerUID {
			continue
		}
		if filter.Status != "" && a.Status != filter.Status {
			continue
		}
		if filter.Kind != "" && a.Kind != filter.Kind {
			continue
		}
		if filter.Search != "" && !strings.Contains(strings.ToLower(a.Name+" "+a.Description), strings.ToLower(filter.Search)) {
			continue
		}
		cp := *a
		out = append(out, &cp)
	}
	// offset + limit
	if filter.Offset > 0 {
		if filter.Offset >= len(out) {
			return []*agent.Agent{}, nil
		}
		out = out[filter.Offset:]
	}
	if filter.Limit > 0 && len(out) > filter.Limit {
		out = out[:filter.Limit]
	}
	return out, nil
}

// maybeSeedAllVirtuals 给 userRepo 里所有用户补 virtual-user agent 行。
// 这在 List / GetByAgentID 之前调用一次，避免测试拿到半态。
func (r *memAgentRepo) maybeSeedAllVirtuals() {
	r.userRepo.mu.Lock()
	for uid := range r.userRepo.byID {
		r.userRepo.mu.Unlock()
		r.maybeSeedVirtual(uid)
		r.userRepo.mu.Lock()
	}
	r.userRepo.mu.Unlock()
}

// memSkillRepo 实现 skill.Repo。
type memSkillRepo struct {
	mu    sync.Mutex
	byKey map[string][]*skill.Skill // agent_id → skills
	next  int64
}

func newMemSkillRepo() *memSkillRepo {
	return &memSkillRepo{byKey: map[string][]*skill.Skill{}}
}

func (r *memSkillRepo) ReplaceByAgentID(_ context.Context, agentID string, in []skill.Input) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*skill.Skill, 0, len(in))
	for _, s := range in {
		r.next++
		out = append(out, &skill.Skill{
			ID:          r.next,
			AgentID:     agentID,
			SkillID:     s.SkillID,
			Name:        s.Name,
			Description: s.Description,
			Tags:        s.Tags,
			InputModes:  s.InputModes,
			OutputModes: s.OutputModes,
		})
	}
	r.byKey[agentID] = out
	return nil
}

func (r *memSkillRepo) ListByAgentID(_ context.Context, agentID string) ([]*skill.Skill, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*skill.Skill, 0, len(r.byKey[agentID]))
	for _, s := range r.byKey[agentID] {
		cp := *s
		out = append(out, &cp)
	}
	return out, nil
}

func (r *memSkillRepo) ListByAgentIDs(_ context.Context, ids []string) (map[string][]*skill.Skill, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[string][]*skill.Skill, len(ids))
	for _, id := range ids {
		if rows, ok := r.byKey[id]; ok {
			cps := make([]*skill.Skill, 0, len(rows))
			for _, s := range rows {
				cp := *s
				cps = append(cps, &cp)
			}
			out[id] = cps
		}
	}
	return out, nil
}

// memAPIKeyRepo 实现 apikey.Repo。
type memAPIKeyRepo struct {
	mu     sync.Mutex
	byID   map[int64]*apikey.Key
	byHash map[string]int64
	nextID int64
}

func newMemAPIKeyRepo() *memAPIKeyRepo {
	return &memAPIKeyRepo{byID: map[int64]*apikey.Key{}, byHash: map[string]int64{}}
}

func (r *memAPIKeyRepo) Insert(_ context.Context, uid int64, hash, prefix, label string) (*apikey.Key, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.nextID++
	k := &apikey.Key{
		ID:        r.nextID,
		OwnerUID:  uid,
		KeyPrefix: prefix,
		Label:     label,
		CreatedAt: time.Now(),
	}
	r.byID[k.ID] = k
	r.byHash[hash] = k.ID
	return cloneAPIKey(k), nil
}

func (r *memAPIKeyRepo) FindByHash(_ context.Context, hash string) (*apikey.Key, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	id, ok := r.byHash[hash]
	if !ok {
		return nil, apikey.ErrKeyNotFound
	}
	return cloneAPIKey(r.byID[id]), nil
}

func (r *memAPIKeyRepo) ListByOwner(_ context.Context, uid int64) ([]*apikey.Key, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*apikey.Key, 0)
	for _, k := range r.byID {
		if k.OwnerUID == uid {
			out = append(out, cloneAPIKey(k))
		}
	}
	return out, nil
}

func (r *memAPIKeyRepo) Revoke(_ context.Context, uid, id int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	k, ok := r.byID[id]
	if !ok || k.OwnerUID != uid {
		return apikey.ErrKeyNotFound
	}
	if k.RevokedAt != nil {
		return nil
	}
	now := time.Now()
	k.RevokedAt = &now
	return nil
}

func (r *memAPIKeyRepo) TouchLastUsed(_ context.Context, id int64, ts time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if k, ok := r.byID[id]; ok {
		t := ts
		k.LastUsedAt = &t
	}
	return nil
}

func cloneAPIKey(k *apikey.Key) *apikey.Key {
	cp := *k
	if k.LastUsedAt != nil {
		t := *k.LastUsedAt
		cp.LastUsedAt = &t
	}
	if k.RevokedAt != nil {
		t := *k.RevokedAt
		cp.RevokedAt = &t
	}
	return &cp
}

// memFriendRepo 实现 friendship.Repo。
type memFriendRepo struct {
	mu   sync.Mutex
	rows map[int64]*friendship.Friendship
	next int64
}

func newMemFriendRepo() *memFriendRepo {
	return &memFriendRepo{rows: map[int64]*friendship.Friendship{}}
}

func (r *memFriendRepo) GetByPair(_ context.Context, from, to string) (*friendship.Friendship, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, f := range r.rows {
		if f.FromAgentID == from && f.ToAgentID == to {
			cp := *f
			return &cp, nil
		}
	}
	return nil, friendship.ErrNotFound
}

func (r *memFriendRepo) GetByID(_ context.Context, id int64) (*friendship.Friendship, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	f, ok := r.rows[id]
	if !ok {
		return nil, friendship.ErrNotFound
	}
	cp := *f
	return &cp, nil
}

func (r *memFriendRepo) Insert(_ context.Context, from, to, reason string) (*friendship.Friendship, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, f := range r.rows {
		if f.FromAgentID == from && f.ToAgentID == to {
			return nil, fmt.Errorf("uk_pair")
		}
	}
	r.next++
	f := &friendship.Friendship{
		ID: r.next, FromAgentID: from, ToAgentID: to,
		Status: friendship.StatusPending, Reason: reason,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	r.rows[r.next] = f
	cp := *f
	return &cp, nil
}

func (r *memFriendRepo) UpdateToPending(_ context.Context, id int64, reason string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	f, ok := r.rows[id]
	if !ok {
		return false, nil
	}
	if f.Status != friendship.StatusRejected && f.Status != friendship.StatusRevoked {
		return false, nil
	}
	f.Status = friendship.StatusPending
	f.Reason = reason
	f.UpdatedAt = time.Now()
	return true, nil
}

func (r *memFriendRepo) UpdateStatus(_ context.Context, id int64, from, to friendship.Status) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	f, ok := r.rows[id]
	if !ok || f.Status != from {
		return false, nil
	}
	f.Status = to
	f.UpdatedAt = time.Now()
	return true, nil
}

func (r *memFriendRepo) ListInvolvingAgent(_ context.Context, id string) ([]*friendship.Friendship, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*friendship.Friendship, 0)
	for _, f := range r.rows {
		if f.Involves(id) {
			cp := *f
			out = append(out, &cp)
		}
	}
	return out, nil
}

func (r *memFriendRepo) ListIncomingPending(_ context.Context, id string) ([]*friendship.Friendship, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*friendship.Friendship, 0)
	for _, f := range r.rows {
		if f.ToAgentID == id && f.Status == friendship.StatusPending {
			cp := *f
			out = append(out, &cp)
		}
	}
	return out, nil
}

func (r *memFriendRepo) ExistsAccepted(_ context.Context, a, b string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, f := range r.rows {
		if f.Status != friendship.StatusAccepted {
			continue
		}
		if (f.FromAgentID == a && f.ToAgentID == b) ||
			(f.FromAgentID == b && f.ToAgentID == a) {
			return true, nil
		}
	}
	return false, nil
}

// agentLookupFromRepo 让 friendship 能查 agent。
type agentLookupFromRepo struct{ repo *memAgentRepo }

func (a *agentLookupFromRepo) Lookup(ctx context.Context, id string) (int64, string, bool) {
	ag, err := a.repo.GetByAgentID(ctx, id)
	if err != nil {
		return 0, "", false
	}
	return ag.OwnerUID, string(ag.Kind), true
}

// 让 strconv / encoding/json 的未使用 import 报错消失（linter）
var (
	_ = strconv.Itoa
	_ = json.RawMessage{}
)
