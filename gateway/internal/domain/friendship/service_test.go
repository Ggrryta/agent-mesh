package friendship

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// ── memRepo：service 测试用的内存实现 ───────────────────────────────────

type memRepo struct {
	mu     sync.Mutex
	next   int64
	rows   map[int64]*Friendship
	byPair map[string]int64 // "from|to" → id
}

func newMemRepo() *memRepo {
	return &memRepo{rows: map[int64]*Friendship{}, byPair: map[string]int64{}}
}

func pairKey(from, to string) string { return from + "|" + to }

func (r *memRepo) GetByPair(_ context.Context, from, to string) (*Friendship, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	id, ok := r.byPair[pairKey(from, to)]
	if !ok {
		return nil, ErrNotFound
	}
	return cloneFriendship(r.rows[id]), nil
}

func (r *memRepo) GetByID(_ context.Context, id int64) (*Friendship, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	f, ok := r.rows[id]
	if !ok {
		return nil, ErrNotFound
	}
	return cloneFriendship(f), nil
}

func (r *memRepo) Insert(_ context.Context, from, to, reason string) (*Friendship, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.byPair[pairKey(from, to)]; dup {
		return nil, errors.New("uk_pair dup")
	}
	r.next++
	f := &Friendship{
		ID:          r.next,
		FromAgentID: from,
		ToAgentID:   to,
		Status:      StatusPending,
		Reason:      reason,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
	r.rows[r.next] = f
	r.byPair[pairKey(from, to)] = r.next
	return cloneFriendship(f), nil
}

func (r *memRepo) UpdateToPending(_ context.Context, id int64, reason string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	f, ok := r.rows[id]
	if !ok {
		return false, nil
	}
	if f.Status != StatusRejected && f.Status != StatusRevoked {
		return false, nil
	}
	f.Status = StatusPending
	f.Reason = reason
	f.UpdatedAt = time.Now()
	return true, nil
}

func (r *memRepo) UpdateStatus(_ context.Context, id int64, fromStatus, toStatus Status) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	f, ok := r.rows[id]
	if !ok || f.Status != fromStatus {
		return false, nil
	}
	f.Status = toStatus
	f.UpdatedAt = time.Now()
	return true, nil
}

func (r *memRepo) ListInvolvingAgent(_ context.Context, agentID string) ([]*Friendship, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*Friendship, 0)
	for _, f := range r.rows {
		if f.Involves(agentID) {
			out = append(out, cloneFriendship(f))
		}
	}
	return out, nil
}

func (r *memRepo) ListIncomingPending(_ context.Context, agentID string) ([]*Friendship, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*Friendship, 0)
	for _, f := range r.rows {
		if f.ToAgentID == agentID && f.Status == StatusPending {
			out = append(out, cloneFriendship(f))
		}
	}
	return out, nil
}

func (r *memRepo) ExistsAccepted(_ context.Context, a, b string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, f := range r.rows {
		if f.Status != StatusAccepted {
			continue
		}
		if (f.FromAgentID == a && f.ToAgentID == b) ||
			(f.FromAgentID == b && f.ToAgentID == a) {
			return true, nil
		}
	}
	return false, nil
}

func cloneFriendship(f *Friendship) *Friendship {
	if f == nil {
		return nil
	}
	c := *f
	return &c
}

// ── memAgents：AgentLookup 测试实现 ─────────────────────────────────────

type memAgents map[string]struct {
	ownerUID int64
	kind     string
}

func (m memAgents) Lookup(_ context.Context, id string) (int64, string, bool) {
	e, ok := m[id]
	if !ok {
		return 0, "", false
	}
	return e.ownerUID, e.kind, true
}

func defaultAgents() memAgents {
	return memAgents{
		"alice":          {ownerUID: 1, kind: "normal"},
		"bob":            {ownerUID: 2, kind: "normal"},
		"carol":          {ownerUID: 1, kind: "normal"}, // alice 和 carol 同 owner
		"virtual-user-1": {ownerUID: 1, kind: "virtual-user"},
		"virtual-user-2": {ownerUID: 2, kind: "virtual-user"},
	}
}

// ── 测试用例 ────────────────────────────────────────────────────────────

func mustRequest(t *testing.T, s *Service, uid int64, from, to, reason string) *Friendship {
	t.Helper()
	f, err := s.Request(context.Background(), uid, from, to, reason)
	if err != nil {
		t.Fatalf("Request(%s→%s): %v", from, to, err)
	}
	return f
}

func TestRequest_HappyPath(t *testing.T) {
	s := NewService(newMemRepo(), defaultAgents())
	f := mustRequest(t, s, 1, "alice", "bob", "hi")
	if f.Status != StatusPending || f.FromAgentID != "alice" || f.ToAgentID != "bob" {
		t.Fatalf("unexpected: %+v", f)
	}
}

func TestRequest_RejectsSelf(t *testing.T) {
	s := NewService(newMemRepo(), defaultAgents())
	_, err := s.Request(context.Background(), 1, "alice", "alice", "")
	if !errors.Is(err, ErrSelfFriend) {
		t.Fatalf("want ErrSelfFriend, got %v", err)
	}
}

func TestRequest_RejectsNotOwner(t *testing.T) {
	s := NewService(newMemRepo(), defaultAgents())
	// user 2 试图以 alice (owner=1) 的身份发请求
	_, err := s.Request(context.Background(), 2, "alice", "bob", "")
	if !errors.Is(err, ErrNotOwner) {
		t.Fatalf("want ErrNotOwner, got %v", err)
	}
}

func TestRequest_RejectsVirtualUserOnEitherSide(t *testing.T) {
	s := NewService(newMemRepo(), defaultAgents())

	_, err := s.Request(context.Background(), 1, "virtual-user-1", "bob", "")
	if !errors.Is(err, ErrVirtualUserPeer) {
		t.Fatalf("virtual as from: want ErrVirtualUserPeer, got %v", err)
	}
	_, err = s.Request(context.Background(), 1, "alice", "virtual-user-2", "")
	if !errors.Is(err, ErrVirtualUserPeer) {
		t.Fatalf("virtual as to: want ErrVirtualUserPeer, got %v", err)
	}
}

func TestRequest_RejectsUnknownAgent(t *testing.T) {
	s := NewService(newMemRepo(), defaultAgents())
	_, err := s.Request(context.Background(), 1, "alice", "ghost", "")
	if !errors.Is(err, ErrInvalidAgent) {
		t.Fatalf("want ErrInvalidAgent, got %v", err)
	}
}

func TestRequest_DuplicatePending(t *testing.T) {
	s := NewService(newMemRepo(), defaultAgents())
	mustRequest(t, s, 1, "alice", "bob", "first")
	_, err := s.Request(context.Background(), 1, "alice", "bob", "second")
	if !errors.Is(err, ErrAlreadyPending) {
		t.Fatalf("want ErrAlreadyPending, got %v", err)
	}
}

func TestRequest_AlreadyAccepted(t *testing.T) {
	s := NewService(newMemRepo(), defaultAgents())
	f := mustRequest(t, s, 1, "alice", "bob", "")
	if _, err := s.Accept(context.Background(), 2, f.ID); err != nil {
		t.Fatalf("accept: %v", err)
	}
	_, err := s.Request(context.Background(), 1, "alice", "bob", "")
	if !errors.Is(err, ErrAlreadyAccepted) {
		t.Fatalf("want ErrAlreadyAccepted, got %v", err)
	}
}

func TestRequest_CoversRejected(t *testing.T) {
	s := NewService(newMemRepo(), defaultAgents())
	f := mustRequest(t, s, 1, "alice", "bob", "v1")
	if _, err := s.Reject(context.Background(), 2, f.ID); err != nil {
		t.Fatalf("reject: %v", err)
	}
	again, err := s.Request(context.Background(), 1, "alice", "bob", "v2")
	if err != nil {
		t.Fatalf("re-request: %v", err)
	}
	if again.Status != StatusPending || again.Reason != "v2" || again.ID != f.ID {
		t.Fatalf("expected same row pending with new reason, got %+v", again)
	}
}

func TestRequest_CoversRevoked(t *testing.T) {
	s := NewService(newMemRepo(), defaultAgents())
	f := mustRequest(t, s, 1, "alice", "bob", "")
	if _, err := s.Accept(context.Background(), 2, f.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Revoke(context.Background(), 1, f.ID); err != nil {
		t.Fatal(err)
	}
	again, err := s.Request(context.Background(), 1, "alice", "bob", "take 2")
	if err != nil {
		t.Fatalf("re-request after revoke: %v", err)
	}
	if again.Status != StatusPending || again.Reason != "take 2" {
		t.Fatalf("unexpected: %+v", again)
	}
}

// ── Accept / Reject / Revoke ───────────────────────────────────────────

func TestAccept_OnlyByReceiver(t *testing.T) {
	s := NewService(newMemRepo(), defaultAgents())
	f := mustRequest(t, s, 1, "alice", "bob", "")
	// 发起方（alice 的 owner）不能 Accept
	if _, err := s.Accept(context.Background(), 1, f.ID); !errors.Is(err, ErrNotOwner) {
		t.Fatalf("sender side accept: want ErrNotOwner, got %v", err)
	}
	got, err := s.Accept(context.Background(), 2, f.ID)
	if err != nil {
		t.Fatalf("receiver accept: %v", err)
	}
	if got.Status != StatusAccepted {
		t.Fatalf("status: %v", got.Status)
	}
}

func TestAccept_InvalidTransition(t *testing.T) {
	s := NewService(newMemRepo(), defaultAgents())
	f := mustRequest(t, s, 1, "alice", "bob", "")
	if _, err := s.Reject(context.Background(), 2, f.ID); err != nil {
		t.Fatal(err)
	}
	// 已 rejected 的行再 accept，应拒
	if _, err := s.Accept(context.Background(), 2, f.ID); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("want ErrInvalidTransition, got %v", err)
	}
}

func TestRevoke_AnyOwnerMayRevoke(t *testing.T) {
	s := NewService(newMemRepo(), defaultAgents())
	f := mustRequest(t, s, 1, "alice", "bob", "")
	if _, err := s.Accept(context.Background(), 2, f.ID); err != nil {
		t.Fatal(err)
	}
	// receiver-side owner 也能 revoke
	if _, err := s.Revoke(context.Background(), 2, f.ID); err != nil {
		t.Fatalf("receiver revoke: %v", err)
	}
}

func TestRevoke_OnlyOnAccepted(t *testing.T) {
	s := NewService(newMemRepo(), defaultAgents())
	f := mustRequest(t, s, 1, "alice", "bob", "")
	// pending 不能被 revoke
	if _, err := s.Revoke(context.Background(), 1, f.ID); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("want ErrInvalidTransition, got %v", err)
	}
}

// ── AreFriends 隐式 + 显式 ─────────────────────────────────────────────

func TestAreFriends_ImplicitVirtualUser(t *testing.T) {
	s := NewService(newMemRepo(), defaultAgents())
	// virtual-user-1 和 alice 同 owner，默认互为好友
	ok, err := s.AreFriends(context.Background(), "virtual-user-1", "alice")
	if err != nil || !ok {
		t.Fatalf("want true, got %v %v", ok, err)
	}
	// virtual-user-1 和 carol 同 owner，也默认互为好友
	ok, err = s.AreFriends(context.Background(), "virtual-user-1", "carol")
	if err != nil || !ok {
		t.Fatalf("want true, got %v %v", ok, err)
	}
	// virtual-user-1 和 bob 不同 owner，没显式 friendship 时 false
	ok, err = s.AreFriends(context.Background(), "virtual-user-1", "bob")
	if err != nil || ok {
		t.Fatalf("want false, got %v %v", ok, err)
	}
}

func TestAreFriends_TwoVirtualUsersNotImplicit(t *testing.T) {
	s := NewService(newMemRepo(), defaultAgents())
	// 两个 virtual-user 之间不走隐式
	ok, err := s.AreFriends(context.Background(), "virtual-user-1", "virtual-user-2")
	if err != nil || ok {
		t.Fatalf("want false, got %v %v", ok, err)
	}
}

func TestAreFriends_AcceptedThenRevoked(t *testing.T) {
	s := NewService(newMemRepo(), defaultAgents())
	f := mustRequest(t, s, 1, "alice", "bob", "")
	if _, err := s.Accept(context.Background(), 2, f.ID); err != nil {
		t.Fatal(err)
	}
	ok, _ := s.AreFriends(context.Background(), "alice", "bob")
	if !ok {
		t.Fatal("accepted should be friends")
	}
	// 反向也 true
	ok, _ = s.AreFriends(context.Background(), "bob", "alice")
	if !ok {
		t.Fatal("accepted should be friends both directions")
	}
	if _, err := s.Revoke(context.Background(), 1, f.ID); err != nil {
		t.Fatal(err)
	}
	ok, _ = s.AreFriends(context.Background(), "alice", "bob")
	if ok {
		t.Fatal("revoked should not be friends")
	}
}

// ── 查询 ────────────────────────────────────────────────────────────────

func TestListFriends_OnlyOwner(t *testing.T) {
	s := NewService(newMemRepo(), defaultAgents())
	_, err := s.ListFriends(context.Background(), 2, "alice", "")
	if !errors.Is(err, ErrNotOwner) {
		t.Fatalf("want ErrNotOwner, got %v", err)
	}
}

func TestListFriends_StatusFilter(t *testing.T) {
	s := NewService(newMemRepo(), defaultAgents())
	// alice 作为 from → bob (pending)
	f1 := mustRequest(t, s, 1, "alice", "bob", "")
	// alice 作为 from → carol，accept 后
	// （carol 同 owner=1，accept 也是 uid=1）
	f2 := mustRequest(t, s, 1, "alice", "carol", "")
	if _, err := s.Accept(context.Background(), 1, f2.ID); err != nil {
		t.Fatal(err)
	}

	all, err := s.ListFriends(context.Background(), 1, "alice", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("want 2 rows, got %d", len(all))
	}

	pending, _ := s.ListFriends(context.Background(), 1, "alice", StatusPending)
	if len(pending) != 1 || pending[0].ID != f1.ID {
		t.Fatalf("pending filter: %v", pending)
	}
	accepted, _ := s.ListFriends(context.Background(), 1, "alice", StatusAccepted)
	if len(accepted) != 1 || accepted[0].ID != f2.ID {
		t.Fatalf("accepted filter: %v", accepted)
	}
}

func TestListIncomingPending(t *testing.T) {
	s := NewService(newMemRepo(), defaultAgents())
	mustRequest(t, s, 1, "alice", "bob", "to bob 1")
	// 同 owner=1 的 carol 向 bob 发请求
	mustRequest(t, s, 1, "carol", "bob", "to bob 2")

	list, err := s.ListIncomingPending(context.Background(), 2, "bob")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("want 2 incoming, got %d", len(list))
	}
}
