package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"agent-gateway/internal/model"
	"agent-gateway/internal/repo"
)

// --- stub AgentRepo ---

type stubAgentRepo struct {
	mu     sync.Mutex
	agents map[string]*model.Agent
}

func newStubAgentRepo() *stubAgentRepo {
	return &stubAgentRepo{agents: make(map[string]*model.Agent)}
}

func (r *stubAgentRepo) Create(_ context.Context, a *model.Agent) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.agents[a.AgentID]; ok {
		return repo.ErrDuplicateAgentID
	}
	r.agents[a.AgentID] = a
	return nil
}

func (r *stubAgentRepo) GetByAgentID(_ context.Context, id string) (*model.Agent, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	a, ok := r.agents[id]
	if !ok {
		return nil, repo.ErrAgentNotFound
	}
	return a, nil
}

func (r *stubAgentRepo) UpdateAgentCard(_ context.Context, id string, updates map[string]any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	a, ok := r.agents[id]
	if !ok {
		return repo.ErrAgentNotFound
	}
	if v, ok := updates["status"]; ok {
		a.Status = v.(model.AgentStatus)
	}
	if v, ok := updates["name"]; ok {
		a.Name = v.(string)
	}
	return nil
}

func (r *stubAgentRepo) UpdateStatus(_ context.Context, id string, status model.AgentStatus) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	a, ok := r.agents[id]
	if !ok {
		return repo.ErrAgentNotFound
	}
	a.Status = status
	return nil
}

func (r *stubAgentRepo) UpdateHeartbeat(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	a, ok := r.agents[id]
	if !ok {
		return repo.ErrAgentNotFound
	}
	now := time.Now()
	a.LastHeartbeatAt = &now
	a.Status = model.AgentStatusActive
	return nil
}

func (r *stubAgentRepo) Delete(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.agents[id]; !ok {
		return repo.ErrAgentNotFound
	}
	delete(r.agents, id)
	return nil
}

func (r *stubAgentRepo) List(_ context.Context, _ repo.AgentFilter) ([]*model.Agent, int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var list []*model.Agent
	for _, a := range r.agents {
		if a.Status == model.AgentStatusActive {
			list = append(list, a)
		}
	}
	return list, int64(len(list)), nil
}

func (r *stubAgentRepo) ListStaleAgents(_ context.Context, _ time.Time) ([]*model.Agent, error) {
	return nil, nil
}

// --- stub AgentSkillRepo ---

type stubAgentSkillRepo struct{}

func (r *stubAgentSkillRepo) ReplaceByAgentID(_ context.Context, _ string, _ []*model.AgentSkill) error {
	return nil
}
func (r *stubAgentSkillRepo) ListByAgentID(_ context.Context, _ string) ([]*model.AgentSkill, error) {
	return nil, nil
}

// --- agentServiceForTest wraps AgentService with stub repos ---

func newTestAgentService() (*AgentService, *stubAgentRepo) {
	stub := newStubAgentRepo()
	cache := NewAgentCache(nil)
	svc := &AgentService{
		agentRepo:      stub,
		agentSkillRepo: &stubAgentSkillRepo{},
		cache:          cache,
	}
	return svc, stub
}

// --- tests ---

func TestAgentService_Register(t *testing.T) {
	svc, _ := newTestAgentService()
	ctx := context.Background()

	req := RegisterAgentRequest{
		AgentID: "agent-1",
		AgentCard: AgentCardInput{
			Name:    "Test Agent",
			URL:     "https://agent.example.com/a2a",
			Version: "1.0",
		},
	}

	agent, err := svc.Register(ctx, "app-owner", req)
	if err != nil {
		t.Fatalf("Register failed: %v", err)
	}
	if agent.AgentID != "agent-1" {
		t.Fatalf("expected agent_id=agent-1, got %s", agent.AgentID)
	}
	if agent.Status != model.AgentStatusActive {
		t.Fatalf("expected status=Active after register")
	}
}

func TestAgentService_Register_Idempotent(t *testing.T) {
	svc, _ := newTestAgentService()
	ctx := context.Background()

	req := RegisterAgentRequest{
		AgentID:   "agent-1",
		AgentCard: AgentCardInput{Name: "v1", URL: "https://agent.example.com/a2a"},
	}
	if _, err := svc.Register(ctx, "app-owner", req); err != nil {
		t.Fatal(err)
	}

	req.AgentCard.Name = "v2"
	agent, err := svc.Register(ctx, "app-owner", req)
	if err != nil {
		t.Fatalf("second Register failed: %v", err)
	}
	if agent.Name != "v2" {
		t.Fatalf("expected name=v2 after re-register, got %s", agent.Name)
	}
}

func TestAgentService_Deregister_NotOwner(t *testing.T) {
	svc, _ := newTestAgentService()
	ctx := context.Background()

	req := RegisterAgentRequest{
		AgentID:   "agent-1",
		AgentCard: AgentCardInput{Name: "A", URL: "https://agent.example.com/a2a"},
	}
	if _, err := svc.Register(ctx, "owner", req); err != nil {
		t.Fatal(err)
	}

	err := svc.Deregister(ctx, "agent-1", "other-app")
	if err == nil || err.Error() != "forbidden: not the owner" {
		t.Fatalf("expected forbidden error, got %v", err)
	}
}

func TestAgentService_Drain(t *testing.T) {
	svc, stub := newTestAgentService()
	ctx := context.Background()

	req := RegisterAgentRequest{
		AgentID:   "agent-1",
		AgentCard: AgentCardInput{Name: "A", URL: "https://agent.example.com/a2a"},
	}
	if _, err := svc.Register(ctx, "owner", req); err != nil {
		t.Fatal(err)
	}

	if err := svc.Drain(ctx, "agent-1", "owner"); err != nil {
		t.Fatalf("Drain failed: %v", err)
	}

	a, _ := stub.GetByAgentID(ctx, "agent-1")
	if a.Status != model.AgentStatusDraining {
		t.Fatalf("expected status=Draining, got %v", a.Status)
	}

	// 缓存应已摘除
	if _, ok := svc.cache.Get("agent-1"); ok {
		t.Fatal("expected agent removed from cache after drain")
	}
}

func TestAgentService_Drain_Idempotent(t *testing.T) {
	svc, _ := newTestAgentService()
	ctx := context.Background()

	req := RegisterAgentRequest{
		AgentID:   "agent-1",
		AgentCard: AgentCardInput{Name: "A", URL: "https://agent.example.com/a2a"},
	}
	if _, err := svc.Register(ctx, "owner", req); err != nil {
		t.Fatal(err)
	}

	if err := svc.Drain(ctx, "agent-1", "owner"); err != nil {
		t.Fatal(err)
	}
	// 再次 Drain 应幂等
	if err := svc.Drain(ctx, "agent-1", "owner"); err != nil {
		t.Fatalf("second Drain should be idempotent, got: %v", err)
	}
}

func TestAgentService_Register_Validation(t *testing.T) {
	svc, _ := newTestAgentService()
	ctx := context.Background()

	pushMode := model.DeliveryModePush
	cases := []RegisterAgentRequest{
		{AgentID: "", AgentCard: AgentCardInput{Name: "A", URL: "https://agent.example.com/a2a"}},
		{AgentID: "x", AgentCard: AgentCardInput{Name: "", URL: "https://agent.example.com/a2a"}},
		// push 模式下 URL 必填
		{AgentID: "x", AgentCard: AgentCardInput{Name: "A", URL: ""}, DeliveryMode: &pushMode},
		{AgentID: "x", AgentCard: AgentCardInput{Name: "A", URL: "ftp://agent.example.com/a2a"}, DeliveryMode: &pushMode},
		{AgentID: "x", AgentCard: AgentCardInput{Name: "A", URL: "http://localhost:9000"}, DeliveryMode: &pushMode},
	}
	for _, req := range cases {
		if _, err := svc.Register(ctx, "owner", req); err == nil {
			t.Fatalf("expected validation error for %+v", req)
		}
	}
}

// pull 模式下 URL 可空
func TestAgentService_Register_PullModeNoURL(t *testing.T) {
	svc, _ := newTestAgentService()
	ctx := context.Background()

	pullMode := model.DeliveryModePull
	req := RegisterAgentRequest{
		AgentID:      "pull-agent",
		AgentCard:    AgentCardInput{Name: "Pull", URL: ""},
		DeliveryMode: &pullMode,
	}
	if _, err := svc.Register(ctx, "owner", req); err != nil {
		t.Fatalf("pull mode empty URL should succeed, got: %v", err)
	}
}

func TestAgentService_Register_RejectTakeoverByDifferentOwner(t *testing.T) {
	svc, _ := newTestAgentService()
	ctx := context.Background()

	req := RegisterAgentRequest{
		AgentID:   "agent-1",
		AgentCard: AgentCardInput{Name: "A", URL: "https://agent.example.com/a2a"},
	}
	if _, err := svc.Register(ctx, "owner", req); err != nil {
		t.Fatal(err)
	}

	_, err := svc.Register(ctx, "other-owner", req)
	if !errors.Is(err, ErrForbiddenNotOwner) {
		t.Fatalf("expected ErrForbiddenNotOwner, got %v", err)
	}
}
