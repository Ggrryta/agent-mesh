// Package mesh 的 handler 层单测。
//
// 和 admin 一样走内存 repo + 真实 service 组装。主要覆盖 /auth/token 的
// 错误分支（API Key 格式错 / 吊销 / 非本 owner），业务端点（heartbeat /
// drain）主要验 JWT kind 匹配、agent_id 匹配、NotOwner / NotFound 错误。
package mesh

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
	"github.com/Ggrryta/agent-mesh/gateway/internal/domain/skill"
	"github.com/Ggrryta/agent-mesh/gateway/internal/middleware"
	"github.com/Ggrryta/agent-mesh/gateway/pkg/auth"
)

// ── 测试 harness ──────────────────────────────────────────────────────

type testHarness struct {
	t       *testing.T
	handler http.Handler
	signer  *auth.Signer

	agents    *agent.Service
	keys      *apikey.Service
	agentRepo *memAgentRepo
	keyRepo   *memAPIKeyRepo
}

func newTestHarness(t *testing.T) *testHarness {
	t.Helper()
	signer, err := auth.NewSigner("test_secret_for_mesh_handler______", time.Hour, time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	agentRepo := newMemAgentRepo()
	agentCache := agent.NewCache()
	skillRepo := newMemSkillRepo()
	agentSvc := agent.NewService(agentRepo, agentCache).WithSkills(skill.NewAdapter(skillRepo))

	keyRepo := newMemAPIKeyRepo()
	keySvc := apikey.NewService(keyRepo, nil)

	h := New(agentSvc, keySvc, nil, nil, signer)
	return &testHarness{
		t:         t,
		handler:   h.Mux(),
		signer:    signer,
		agents:    agentSvc,
		keys:      keySvc,
		agentRepo: agentRepo,
		keyRepo:   keyRepo,
	}
}

// seedAgent 直接往 agentRepo 插行，跳过 Register。
func (h *testHarness) seedAgent(agentID string, ownerUID int64, kind agent.Kind) {
	h.t.Helper()
	_ = h.agentRepo.Upsert(context.Background(), &agent.Agent{
		AgentID:  agentID,
		OwnerUID: ownerUID,
		Name:     agentID,
		Kind:     kind,
		Status:   agent.StatusActive,
	})
}

// issueAPIKey 以 owner 身份签发一把 key，返回原始 key 明文。
func (h *testHarness) issueAPIKey(ownerUID int64) string {
	h.t.Helper()
	raw, _, err := h.keys.Issue(context.Background(), ownerUID, "test")
	if err != nil {
		h.t.Fatal(err)
	}
	return raw
}

// do 发请求。如果 rawKey 非空走 X-Api-Key；authToken 非空则解析 JWT 注入 X-Mesh-* header。
func (h *testHarness) do(method, path, authToken, rawKey string, body any) *httptest.ResponseRecorder {
	h.t.Helper()
	var r *http.Request
	if body != nil {
		b, _ := json.Marshal(body)
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
	if rawKey != "" {
		r.Header.Set("X-Api-Key", rawKey)
	}
	rr := httptest.NewRecorder()
	h.handler.ServeHTTP(rr, r)
	return rr
}

// ── /auth/token ────────────────────────────────────────────────────────

func TestAuthToken_HappyPath(t *testing.T) {
	h := newTestHarness(t)
	h.seedAgent("alice-dev", 1, agent.KindNormal)
	raw := h.issueAPIKey(1)

	rr := h.do(http.MethodPost, "/auth/token", "", raw, map[string]string{
		"agent_id": "alice-dev",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	var resp struct {
		Token     string `json:"token"`
		ExpiresIn int    `json:"expires_in"`
		AgentID   string `json:"agent_id"`
	}
	mustDecode(t, rr, &resp)
	if resp.Token == "" || resp.ExpiresIn <= 0 || resp.AgentID != "alice-dev" {
		t.Fatalf("unexpected: %+v", resp)
	}
	// 签出的 token 解出来 kind=agent + agent_id + key_id。
	claims, err := h.signer.Verify(resp.Token)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if claims.Kind != auth.KindAgent || claims.AgentID != "alice-dev" || claims.KeyID == 0 {
		t.Fatalf("claims: %+v", claims)
	}
}

func TestAuthToken_MissingHeader(t *testing.T) {
	h := newTestHarness(t)
	rr := h.do(http.MethodPost, "/auth/token", "", "", map[string]string{"agent_id": "x"})
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", rr.Code)
	}
	if c := errCode(t, rr); c != 40111 {
		t.Fatalf("want code 40111, got %d", c)
	}
}

func TestAuthToken_BadBody(t *testing.T) {
	h := newTestHarness(t)
	h.seedAgent("alice-dev", 1, agent.KindNormal)
	raw := h.issueAPIKey(1)

	// body 不是对象 → 400
	rr := h.do(http.MethodPost, "/auth/token", "", raw, "nope")
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("bad body: want 400, got %d", rr.Code)
	}

	// body 少 agent_id → 400
	rr = h.do(http.MethodPost, "/auth/token", "", raw, map[string]string{})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("missing agent_id: want 400, got %d", rr.Code)
	}
}

func TestAuthToken_InvalidKey(t *testing.T) {
	h := newTestHarness(t)
	h.seedAgent("alice-dev", 1, agent.KindNormal)

	rr := h.do(http.MethodPost, "/auth/token", "",
		"sk-am_definitely-not-a-real-key", map[string]string{"agent_id": "alice-dev"})
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("unknown key: want 401, got %d", rr.Code)
	}
	if c := errCode(t, rr); c != 40111 {
		t.Fatalf("want 40111, got %d", c)
	}
}

func TestAuthToken_RevokedKey(t *testing.T) {
	h := newTestHarness(t)
	h.seedAgent("alice-dev", 1, agent.KindNormal)
	raw := h.issueAPIKey(1)

	// 吊销：通过 service 直接调。
	list, err := h.keys.List(context.Background(), 1)
	if err != nil || len(list) == 0 {
		t.Fatalf("list: %v", err)
	}
	if err := h.keys.Revoke(context.Background(), 1, list[0].ID); err != nil {
		t.Fatal(err)
	}

	rr := h.do(http.MethodPost, "/auth/token", "", raw, map[string]string{"agent_id": "alice-dev"})
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("revoked: want 401, got %d", rr.Code)
	}
	if c := errCode(t, rr); c != 40112 {
		t.Fatalf("want 40112, got %d", c)
	}
}

func TestAuthToken_AgentNotOwned(t *testing.T) {
	h := newTestHarness(t)
	// alice 的 key，但请求的 agent 是 bob 的
	h.seedAgent("alice-dev", 1, agent.KindNormal)
	h.seedAgent("bob-dev", 2, agent.KindNormal)
	raw := h.issueAPIKey(1)

	rr := h.do(http.MethodPost, "/auth/token", "", raw, map[string]string{
		"agent_id": "bob-dev",
	})
	if rr.Code != http.StatusForbidden {
		t.Fatalf("want 403, got %d", rr.Code)
	}
	if c := errCode(t, rr); c != 40113 {
		t.Fatalf("want 40113, got %d", c)
	}

	// agent 根本不存在也返相同的错误（防枚举）
	rr = h.do(http.MethodPost, "/auth/token", "", raw, map[string]string{
		"agent_id": "ghost",
	})
	if rr.Code != http.StatusForbidden {
		t.Fatalf("ghost: want 403, got %d", rr.Code)
	}
}

// ── 业务端点 ──────────────────────────────────────────────────────────

func TestHeartbeat_OK(t *testing.T) {
	h := newTestHarness(t)
	h.seedAgent("alice-dev", 1, agent.KindNormal)
	tok, _ := h.signer.IssueAgent("alice-dev", 1, 99)

	rr := h.do(http.MethodPost, "/agents/alice-dev/heartbeat", tok, "", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestHeartbeat_MismatchedAgentID(t *testing.T) {
	h := newTestHarness(t)
	h.seedAgent("alice-dev", 1, agent.KindNormal)
	h.seedAgent("bob-dev", 2, agent.KindNormal)
	// JWT 代表 alice-dev，但打的 path 是 bob-dev
	tok, _ := h.signer.IssueAgent("alice-dev", 1, 99)

	rr := h.do(http.MethodPost, "/agents/bob-dev/heartbeat", tok, "", nil)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("want 403, got %d", rr.Code)
	}
}

func TestHeartbeat_WrongTokenKind(t *testing.T) {
	h := newTestHarness(t)
	h.seedAgent("alice-dev", 1, agent.KindNormal)
	// 用 user token 打 mesh 端点
	userTok, _ := h.signer.IssueUser(1)

	rr := h.do(http.MethodPost, "/agents/alice-dev/heartbeat", userTok, "", nil)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("want 403, got %d", rr.Code)
	}
}

func TestDrain_OK(t *testing.T) {
	h := newTestHarness(t)
	h.seedAgent("alice-dev", 1, agent.KindNormal)
	tok, _ := h.signer.IssueAgent("alice-dev", 1, 99)

	rr := h.do(http.MethodPost, "/agents/alice-dev/drain", tok, "", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d body=%s", rr.Code, rr.Body.String())
	}
}

// ── helpers ────────────────────────────────────────────────────────────

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

// ── 内存 repo ──────────────────────────────────────────────────────────

type memAgentRepo struct {
	mu   sync.Mutex
	byID map[string]*agent.Agent
}

func newMemAgentRepo() *memAgentRepo {
	return &memAgentRepo{byID: map[string]*agent.Agent{}}
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
	existing, ok := r.byID[a.AgentID]
	if !ok {
		cp := *a
		r.byID[a.AgentID] = &cp
		return nil
	}
	existing.Name = a.Name
	existing.URL = a.URL
	existing.Status = a.Status
	return nil
}

func (r *memAgentRepo) GetByAgentID(_ context.Context, id string) (*agent.Agent, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
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
	out := make([]*agent.Agent, 0)
	for _, a := range r.byID {
		if filter.Status != "" && a.Status != filter.Status {
			continue
		}
		if filter.Kind != "" && a.Kind != filter.Kind {
			continue
		}
		cp := *a
		out = append(out, &cp)
	}
	return out, nil
}

// memSkillRepo
type memSkillRepo struct{}

func newMemSkillRepo() *memSkillRepo { return &memSkillRepo{} }

func (r *memSkillRepo) ReplaceByAgentID(_ context.Context, _ string, _ []skill.Input) error {
	return nil
}
func (r *memSkillRepo) ListByAgentID(_ context.Context, _ string) ([]*skill.Skill, error) {
	return nil, nil
}
func (r *memSkillRepo) ListByAgentIDs(_ context.Context, _ []string) (map[string][]*skill.Skill, error) {
	return map[string][]*skill.Skill{}, nil
}

// memAPIKeyRepo
type memAPIKeyRepo struct {
	mu     sync.Mutex
	byID   map[int64]*apikey.Key
	byHash map[string]int64
	next   int64
}

func newMemAPIKeyRepo() *memAPIKeyRepo {
	return &memAPIKeyRepo{byID: map[int64]*apikey.Key{}, byHash: map[string]int64{}}
}

func (r *memAPIKeyRepo) Insert(_ context.Context, uid int64, hash, prefix, label string) (*apikey.Key, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.next++
	k := &apikey.Key{
		ID: r.next, OwnerUID: uid, KeyPrefix: prefix, Label: label,
		CreatedAt: time.Now(),
	}
	r.byID[k.ID] = k
	r.byHash[hash] = k.ID
	cp := *k
	return &cp, nil
}

func (r *memAPIKeyRepo) FindByHash(_ context.Context, hash string) (*apikey.Key, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	id, ok := r.byHash[hash]
	if !ok {
		return nil, apikey.ErrKeyNotFound
	}
	cp := *r.byID[id]
	return &cp, nil
}

func (r *memAPIKeyRepo) ListByOwner(_ context.Context, uid int64) ([]*apikey.Key, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*apikey.Key, 0)
	for _, k := range r.byID {
		if k.OwnerUID == uid {
			cp := *k
			out = append(out, &cp)
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

// 避免 fmt / strings 不用报错。
var (
	_ = fmt.Sprintf
	_ = strings.HasPrefix
)
