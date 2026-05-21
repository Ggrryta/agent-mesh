package delivery

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Ggrryta/agent-mesh/gateway/internal/domain/inbox"
	"github.com/Ggrryta/agent-mesh/gateway/internal/domain/task"

	"go.uber.org/zap"
)

// stubLookup 实现 AgentURLLookup。
type stubLookup struct {
	urls map[string]string
}

func (s stubLookup) LookupURL(_ context.Context, id string) (string, bool) {
	u, ok := s.urls[id]
	return u, ok
}

// stubInboxRepo：Pusher 只需要 inbox.Service.MarkDelivered，
// 所以给 inbox repo 写一个最小 stub 支持那个。
type stubInboxRepo struct {
	mu     sync.Mutex
	marked []int64
}

func (s *stubInboxRepo) Insert(_ context.Context, e *inbox.Event) (*inbox.Event, error) {
	// 不在 push 测试里用到；给个合法返回。
	return e, nil
}

func (s *stubInboxRepo) ListSince(_ context.Context, _ string, _ int64, _ int) ([]*inbox.Event, error) {
	return nil, nil
}

func (s *stubInboxRepo) MarkDelivered(_ context.Context, ids []int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.marked = append(s.marked, ids...)
	return nil
}

func newSvc() (*inbox.Service, *stubInboxRepo) {
	r := &stubInboxRepo{}
	return inbox.NewService(r), r
}

// 构造一个 Event 便于测试。
func newEvent(id int64, agent string) *inbox.Event {
	payload, _ := json.Marshal(&task.Message{
		MessageID: "m1", TaskID: "t1", ContextID: "t1",
		Role: task.RoleUser, Parts: []task.Part{{Kind: task.PartText, Text: "hi"}},
	})
	return &inbox.Event{
		ID: id, AgentID: agent, Kind: inbox.KindMessage,
		TaskID: "t1", RefID: "m1", Payload: payload,
		CreatedAt: time.Now(),
	}
}

// ─── 成功路径：push 200 后 MarkDelivered ────────────────────────

func TestPush_Success_MarksDelivered(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.Path != "/a2a/events" {
			t.Errorf("path: %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	svc, repo := newSvc()
	p := NewPusher(svc, stubLookup{urls: map[string]string{"bob": srv.URL}},
		Config{Timeout: time.Second}, zap.NewNop())

	event := newEvent(42, "bob")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.Run(ctx)

	p.NotifyEvent(event)
	// 等 worker 处理
	for i := 0; i < 50; i++ {
		if hits.Load() > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if hits.Load() != 1 {
		t.Fatalf("hits: %d", hits.Load())
	}
	// MarkDelivered 也调过
	for i := 0; i < 50; i++ {
		repo.mu.Lock()
		n := len(repo.marked)
		repo.mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	repo.mu.Lock()
	got := append([]int64(nil), repo.marked...)
	repo.mu.Unlock()
	if len(got) != 1 || got[0] != 42 {
		t.Fatalf("marked: %+v", got)
	}
}

// ─── 失败路径：agent 返 500 → 不 MarkDelivered ───────────────

func TestPush_Failure_NoMarkDelivered(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	svc, repo := newSvc()
	p := NewPusher(svc, stubLookup{urls: map[string]string{"bob": srv.URL}},
		Config{Timeout: time.Second}, zap.NewNop())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.Run(ctx)

	p.NotifyEvent(newEvent(1, "bob"))
	time.Sleep(300 * time.Millisecond)
	repo.mu.Lock()
	n := len(repo.marked)
	repo.mu.Unlock()
	if n != 0 {
		t.Fatalf("MarkDelivered should not be called on failure; got %d", n)
	}
}

// ─── 无 URL：跳过，不调 MarkDelivered ───────────────────────

func TestPush_NoURL_SkipSilently(t *testing.T) {
	svc, repo := newSvc()
	p := NewPusher(svc, stubLookup{urls: map[string]string{}}, // 空
		Config{Timeout: time.Second}, zap.NewNop())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.Run(ctx)

	p.NotifyEvent(newEvent(1, "offline-bob"))
	time.Sleep(300 * time.Millisecond)
	repo.mu.Lock()
	n := len(repo.marked)
	repo.mu.Unlock()
	if n != 0 {
		t.Fatalf("no URL should skip, but marked: %d", n)
	}
}

// ─── 队列满：NotifyEvent 不阻塞 ──────────────────────────────

func TestPush_QueueFull_NonBlocking(t *testing.T) {
	svc, _ := newSvc()
	// 只留 1 的深度 + 不启动 Run，channel 满之后 Notify 必须立刻返回
	p := NewPusher(svc, stubLookup{}, Config{QueueDepth: 1}, zap.NewNop())

	done := make(chan struct{})
	go func() {
		for i := 0; i < 10; i++ {
			p.NotifyEvent(newEvent(int64(i), "bob"))
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("NotifyEvent blocked when queue full")
	}
}
