package agent

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// memRepo 是 service 测试用的线程安全内存 Repo。
// 真实 MySQL 覆盖在 repo_test.go（需要 live DB）。
type memRepo struct {
	mu sync.Mutex
	m  map[string]*Agent
}

func newMemRepo() *memRepo { return &memRepo{m: map[string]*Agent{}} }

func (r *memRepo) clone(a *Agent) *Agent {
	cp := *a
	return &cp
}

func (r *memRepo) Create(_ context.Context, a *Agent) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.m[a.AgentID]; ok {
		return ErrAgentIDExists
	}
	now := time.Now()
	a.CreatedAt = now
	a.UpdatedAt = now
	r.m[a.AgentID] = r.clone(a)
	return nil
}

func (r *memRepo) Upsert(_ context.Context, a *Agent) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	a.UpdatedAt = now
	if existing, ok := r.m[a.AgentID]; ok {
		a.CreatedAt = existing.CreatedAt
	} else {
		a.CreatedAt = now
	}
	r.m[a.AgentID] = r.clone(a)
	return nil
}

func (r *memRepo) GetByAgentID(_ context.Context, id string) (*Agent, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	a, ok := r.m[id]
	if !ok {
		return nil, ErrAgentNotFound
	}
	return r.clone(a), nil
}

func (r *memRepo) UpdateStatus(_ context.Context, id string, s Status) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	a, ok := r.m[id]
	if !ok {
		return ErrAgentNotFound
	}
	a.Status = s
	return nil
}

func (r *memRepo) UpdateHeartbeat(_ context.Context, id string, ts time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	a, ok := r.m[id]
	if !ok {
		return ErrAgentNotFound
	}
	a.LastHeartbeatAt = &ts
	if a.Status == StatusInactive {
		a.Status = StatusActive
	}
	return nil
}

func (r *memRepo) Delete(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.m[id]; !ok {
		return ErrAgentNotFound
	}
	delete(r.m, id)
	return nil
}

func (r *memRepo) List(_ context.Context, f Filter) ([]*Agent, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*Agent, 0, len(r.m))
	for _, a := range r.m {
		if f.OwnerUID != 0 && a.OwnerUID != f.OwnerUID {
			continue
		}
		if f.Status != "" && a.Status != f.Status {
			continue
		}
		if f.Kind != "" && a.Kind != f.Kind {
			continue
		}
		out = append(out, r.clone(a))
	}
	return out, nil
}

func newSvc() (*Service, *Cache) {
	repo := newMemRepo()
	cache := NewCache()
	return NewService(repo, cache), cache
}

func TestService_Register_CreatesAndCachesAgent(t *testing.T) {
	svc, cache := newSvc()
	a, err := svc.Register(context.Background(), RegisterInput{
		AgentID:  "Alice-Dev",
		OwnerUID: 1,
		Name:     "alice",
		URL:      "http://alice:8080",
	})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if a.AgentID != "alice-dev" {
		t.Fatalf("id not normalized: %q", a.AgentID)
	}
	if a.Status != StatusInactive {
		t.Fatalf("status=%v (expected inactive on register, becomes active after first heartbeat)", a.Status)
	}
	if _, ok := cache.GetActive("alice-dev"); ok {
		t.Fatal("inactive agent should not be in active cache")
	}
}

func TestService_Register_RejectsVirtualPrefix(t *testing.T) {
	svc, _ := newSvc()
	_, err := svc.Register(context.Background(), RegisterInput{
		AgentID:  "virtual-user-1",
		OwnerUID: 1,
		Name:     "nope",
	})
	if !errors.Is(err, ErrReservedVirtualName) {
		t.Fatalf("want ErrReservedVirtualName, got %v", err)
	}
}

func TestService_Register_SameOwnerUpdates(t *testing.T) {
	svc, _ := newSvc()
	ctx := context.Background()
	_, err := svc.Register(ctx, RegisterInput{AgentID: "alice", OwnerUID: 1, Name: "v1"})
	if err != nil {
		t.Fatal(err)
	}
	a2, err := svc.Register(ctx, RegisterInput{AgentID: "alice", OwnerUID: 1, Name: "v2", Version: "1.1"})
	if err != nil {
		t.Fatalf("same-owner upsert: %v", err)
	}
	if a2.Name != "v2" || a2.Version != "1.1" {
		t.Fatalf("upsert didn't apply: %+v", a2)
	}
}

func TestService_Register_RejectsDifferentOwner(t *testing.T) {
	svc, _ := newSvc()
	ctx := context.Background()
	_, _ = svc.Register(ctx, RegisterInput{AgentID: "alice", OwnerUID: 1, Name: "v1"})
	_, err := svc.Register(ctx, RegisterInput{AgentID: "alice", OwnerUID: 2, Name: "takeover"})
	if !errors.Is(err, ErrNotOwner) {
		t.Fatalf("want ErrNotOwner, got %v", err)
	}
}

func TestService_Drain_CacheUpdatedToHide(t *testing.T) {
	svc, cache := newSvc()
	ctx := context.Background()
	_, _ = svc.Register(ctx, RegisterInput{AgentID: "alice", OwnerUID: 1, Name: "v1"})
	if err := svc.Drain(ctx, "alice", 1); err != nil {
		t.Fatal(err)
	}
	// Active view should no longer include alice.
	if _, ok := cache.GetActive("alice"); ok {
		t.Fatal("draining agent still exposed as Active")
	}
	// Plain Get should still show it with Draining status for admin UI.
	if a, ok := cache.Get("alice"); !ok || a.Status != StatusDraining {
		t.Fatalf("expected draining row in cache: ok=%v status=%v", ok, a.Status)
	}
}

func TestService_Heartbeat_RecoversFromInactive(t *testing.T) {
	svc, _ := newSvc()
	ctx := context.Background()
	_, _ = svc.Register(ctx, RegisterInput{AgentID: "alice", OwnerUID: 1, Name: "v1"})
	// Manually flip to inactive by driving the repo via a second service
	// call that wouldn't exist normally — use repo directly via type assertion.
	if err := svc.repo.UpdateStatus(ctx, "alice", StatusInactive); err != nil {
		t.Fatal(err)
	}
	a, err := svc.Heartbeat(ctx, "alice", 1)
	if err != nil {
		t.Fatal(err)
	}
	if a.Status != StatusActive {
		t.Fatalf("heartbeat should revive inactive: %v", a.Status)
	}
}

func TestService_Heartbeat_RejectsDifferentOwner(t *testing.T) {
	svc, _ := newSvc()
	ctx := context.Background()
	_, _ = svc.Register(ctx, RegisterInput{AgentID: "alice", OwnerUID: 1, Name: "v1"})
	if _, err := svc.Heartbeat(ctx, "alice", 99); !errors.Is(err, ErrNotOwner) {
		t.Fatalf("want ErrNotOwner, got %v", err)
	}
}

func TestService_Deregister_HappyPath(t *testing.T) {
	svc, cache := newSvc()
	ctx := context.Background()
	_, _ = svc.Register(ctx, RegisterInput{AgentID: "alice", OwnerUID: 1, Name: "v1"})
	if err := svc.Deregister(ctx, "alice", 1); err != nil {
		t.Fatal(err)
	}
	if _, ok := cache.Get("alice"); ok {
		t.Fatal("cache still has deregistered agent")
	}
}

func TestService_ValidateAgentID(t *testing.T) {
	bad := []string{
		"", "ab", "  ", "has space", "UPPER",
		"-leading", "trailing-", "has/slash",
	}
	for _, b := range bad {
		if err := ValidateAgentID(b); err == nil {
			t.Errorf("want error for %q", b)
		}
	}
	good := []string{"alice", "abc", "agent.v2", "a_b-c", "a.b_c-1", "123"}
	for _, g := range good {
		if err := ValidateAgentID(g); err != nil {
			t.Errorf("want ok for %q, got %v", g, err)
		}
	}
}
