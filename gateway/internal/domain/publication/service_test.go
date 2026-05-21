package publication

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Ggrryta/agent-mesh/gateway/internal/domain/agent"
)

// memRepo / fakeAgentLookup / fakeRegistrar 用于测试 service 行为。

type memRepo struct {
	mu       sync.Mutex
	pubs     map[int64]*Publication
	subs     map[int64]*Subscription
	nextPub  int64
	nextSub  int64
	subIndex map[string]bool // 模拟 (uid, publication_id) 唯一键
}

func newMem() *memRepo {
	return &memRepo{pubs: map[int64]*Publication{}, subs: map[int64]*Subscription{}, subIndex: map[string]bool{}}
}

func (r *memRepo) Insert(_ context.Context, p *Publication) (*Publication, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.nextPub++
	cp := *p
	cp.ID = r.nextPub
	cp.CreatedAt = time.Now()
	cp.UpdatedAt = cp.CreatedAt
	r.pubs[cp.ID] = &cp
	out := cp
	return &out, nil
}

func (r *memRepo) GetByID(_ context.Context, id int64) (*Publication, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.pubs[id]
	if !ok {
		return nil, ErrPublicationNotFound
	}
	cp := *p
	return &cp, nil
}

func (r *memRepo) List(_ context.Context, f Filter) ([]*Publication, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*Publication, 0)
	for _, p := range r.pubs {
		if f.PublisherUID != 0 && p.PublisherUID != f.PublisherUID {
			continue
		}
		cp := *p
		out = append(out, &cp)
	}
	return out, nil
}

func (r *memRepo) IncrementDownload(_ context.Context, id int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if p, ok := r.pubs[id]; ok {
		p.DownloadCount++
	}
	return nil
}

func (r *memRepo) DeleteOwned(_ context.Context, id, uid int64) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.pubs[id]
	if !ok || p.PublisherUID != uid {
		return false, nil
	}
	delete(r.pubs, id)
	return true, nil
}

func (r *memRepo) InsertSubscription(_ context.Context, s *Subscription) (*Subscription, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := keySub(s.UID, s.PublicationID)
	if r.subIndex[key] {
		return nil, ErrAlreadySubscribed
	}
	r.nextSub++
	cp := *s
	cp.ID = r.nextSub
	cp.CreatedAt = time.Now()
	r.subs[cp.ID] = &cp
	r.subIndex[key] = true
	return &cp, nil
}

func (r *memRepo) ListSubscriptionsByUser(_ context.Context, uid int64) ([]*Subscription, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := []*Subscription{}
	for _, s := range r.subs {
		if s.UID == uid {
			cp := *s
			out = append(out, &cp)
		}
	}
	return out, nil
}

func (r *memRepo) HasSubscription(_ context.Context, uid, pid int64) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.subIndex[keySub(uid, pid)], nil
}

func keySub(uid, pid int64) string { return string(rune(uid)) + ":" + string(rune(pid)) }

type fakeAgentLookup struct{ a *agent.Agent }

func (f *fakeAgentLookup) Get(_ context.Context, _ string) (*agent.Agent, error) {
	if f.a == nil {
		return nil, agent.ErrAgentNotFound
	}
	cp := *f.a
	return &cp, nil
}

type fakeRegistrar struct{ created []*agent.Agent }

func (f *fakeRegistrar) Register(_ context.Context, in agent.RegisterInput) (*agent.Agent, error) {
	a := &agent.Agent{
		AgentID:      in.AgentID,
		OwnerUID:     in.OwnerUID,
		Name:         in.Name,
		Description:  in.Description,
		SystemPrompt: in.SystemPrompt,
	}
	f.created = append(f.created, a)
	return a, nil
}

// ─── tests ──────────────────────────────────────────────────────────────

func TestPublishAndFork(t *testing.T) {
	repo := newMem()
	src := &agent.Agent{AgentID: "alice", OwnerUID: 1, Name: "Alice", SystemPrompt: "be alice"}
	svc := NewService(repo, &fakeAgentLookup{a: src}, &fakeRegistrar{})

	// publish
	p, err := svc.Publish(context.Background(), PublishInput{
		PublisherUID:  1,
		SourceAgentID: "alice",
		Title:         "Alice the assistant",
		Summary:       "general",
		Category:      "general",
		Tags:          []string{"writing", "research"},
	})
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if p.ID == 0 || p.SystemPromptTemplate != "be alice" {
		t.Fatalf("Publish snapshot wrong: %+v", p)
	}

	// fork by user 2
	sub, created, err := svc.Fork(context.Background(), ForkInput{
		PublicationID: p.ID, ForkerUID: 2, NewAgentID: "alice-fork",
	})
	if err != nil {
		t.Fatalf("Fork: %v", err)
	}
	if sub.ForkedAgentID != "alice-fork" || created.SystemPrompt != "be alice" {
		t.Fatalf("Fork result wrong: sub=%+v agent=%+v", sub, created)
	}
}

func TestForkOwnPublicationRejected(t *testing.T) {
	repo := newMem()
	src := &agent.Agent{AgentID: "alice", OwnerUID: 1, Name: "Alice"}
	svc := NewService(repo, &fakeAgentLookup{a: src}, &fakeRegistrar{})
	p, _ := svc.Publish(context.Background(), PublishInput{PublisherUID: 1, SourceAgentID: "alice", Title: "t"})
	_, _, err := svc.Fork(context.Background(), ForkInput{PublicationID: p.ID, ForkerUID: 1, NewAgentID: "x"})
	if err != ErrCannotForkOwn {
		t.Fatalf("expected ErrCannotForkOwn, got %v", err)
	}
}

func TestForkAlreadySubscribed(t *testing.T) {
	repo := newMem()
	src := &agent.Agent{AgentID: "alice", OwnerUID: 1, Name: "Alice"}
	svc := NewService(repo, &fakeAgentLookup{a: src}, &fakeRegistrar{})
	p, _ := svc.Publish(context.Background(), PublishInput{PublisherUID: 1, SourceAgentID: "alice", Title: "t"})

	if _, _, err := svc.Fork(context.Background(), ForkInput{PublicationID: p.ID, ForkerUID: 2, NewAgentID: "x"}); err != nil {
		t.Fatalf("first fork: %v", err)
	}
	_, _, err := svc.Fork(context.Background(), ForkInput{PublicationID: p.ID, ForkerUID: 2, NewAgentID: "y"})
	if err != ErrAlreadySubscribed {
		t.Fatalf("expected ErrAlreadySubscribed, got %v", err)
	}
}

func TestPublishNotOwner(t *testing.T) {
	repo := newMem()
	src := &agent.Agent{AgentID: "alice", OwnerUID: 1, Name: "Alice"}
	svc := NewService(repo, &fakeAgentLookup{a: src}, &fakeRegistrar{})
	_, err := svc.Publish(context.Background(), PublishInput{PublisherUID: 99, SourceAgentID: "alice", Title: "t"})
	if err != agent.ErrNotOwner {
		t.Fatalf("expected ErrNotOwner, got %v", err)
	}
}

func TestPublishValidation(t *testing.T) {
	repo := newMem()
	src := &agent.Agent{AgentID: "alice", OwnerUID: 1, Name: "Alice"}
	svc := NewService(repo, &fakeAgentLookup{a: src}, &fakeRegistrar{})
	if _, err := svc.Publish(context.Background(), PublishInput{PublisherUID: 1, SourceAgentID: "alice", Title: ""}); err != ErrTitleRequired {
		t.Fatalf("title required: got %v", err)
	}
	long := make([]byte, MaxTitleLen+1)
	for i := range long {
		long[i] = 'x'
	}
	if _, err := svc.Publish(context.Background(), PublishInput{PublisherUID: 1, SourceAgentID: "alice", Title: string(long)}); err != ErrTitleTooLong {
		t.Fatalf("title too long: got %v", err)
	}
}

func TestSerializeParseTags(t *testing.T) {
	tags := []string{"a", "  b  ", "", "c"}
	s := SerializeTags(tags)
	if s != "a,b,c" {
		t.Fatalf("unexpected: %q", s)
	}
	parsed := ParseTags(s)
	if len(parsed) != 3 || parsed[0] != "a" || parsed[2] != "c" {
		t.Fatalf("parse: %v", parsed)
	}
}
