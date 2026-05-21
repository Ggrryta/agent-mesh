package inbox

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/Ggrryta/agent-mesh/gateway/internal/domain/task"

	_ "github.com/go-sql-driver/mysql"
)

// ─── memRepo（单测用）─────────────────────────────────────────────

type memRepo struct {
	mu     sync.Mutex
	rows   map[int64]*Event
	nextID int64
}

func newMemRepo() *memRepo { return &memRepo{rows: map[int64]*Event{}} }

func (r *memRepo) Insert(_ context.Context, e *Event) (*Event, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.nextID++
	cp := *e
	cp.ID = r.nextID
	cp.CreatedAt = time.Now()
	r.rows[cp.ID] = &cp
	result := cp
	return &result, nil
}

func (r *memRepo) ListSince(_ context.Context, agentID string, sinceID int64, limit int) ([]*Event, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if limit <= 0 {
		limit = 100
	}
	out := make([]*Event, 0)
	for id := sinceID + 1; ; id++ {
		if len(out) >= limit {
			break
		}
		e, ok := r.rows[id]
		if !ok {
			// 允许中间 id 空缺；继续往后找
			if id > r.nextID {
				break
			}
			continue
		}
		if e.AgentID != agentID {
			continue
		}
		cp := *e
		out = append(out, &cp)
	}
	return out, nil
}

func (r *memRepo) MarkDelivered(_ context.Context, ids []int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := time.Now()
	for _, id := range ids {
		if e, ok := r.rows[id]; ok {
			t := n
			e.DeliveredAt = &t
		}
	}
	return nil
}

// ─── 单测 ────────────────────────────────────────────────────────

func TestService_EnqueueAndPull(t *testing.T) {
	svc := NewService(newMemRepo())

	msg := &task.Message{
		MessageID: "m1", TaskID: "t1", ContextID: "t1",
		Role:  task.RoleUser,
		Parts: []task.Part{{Kind: task.PartText, Text: "hi"}},
	}
	if err := svc.EnqueueMessage(context.Background(), "bob", msg); err != nil {
		t.Fatal(err)
	}

	events, maxID, err := svc.Pull(context.Background(), "bob", 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Kind != KindMessage {
		t.Fatalf("events: %+v", events)
	}
	if maxID != events[0].ID {
		t.Fatalf("maxID: %d vs %d", maxID, events[0].ID)
	}
	// payload 解回来应和原 message 一致
	var back task.Message
	if err := json.Unmarshal(events[0].Payload, &back); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if back.MessageID != "m1" {
		t.Fatal("payload mismatch")
	}
}

func TestService_EnqueueAllThreeKinds(t *testing.T) {
	svc := NewService(newMemRepo())
	ctx := context.Background()

	_ = svc.EnqueueMessage(ctx, "bob", &task.Message{
		MessageID: "m1", TaskID: "t1", ContextID: "t1",
		Role: task.RoleUser, Parts: []task.Part{{Kind: task.PartText, Text: "x"}},
	})
	_ = svc.EnqueueArtifact(ctx, "alice", &task.Artifact{
		ArtifactID: "a1", TaskID: "t1", ContextID: "t1",
		Parts: []task.Part{{Kind: task.PartText, Text: "result"}},
	})
	_ = svc.EnqueueTransition(ctx, "alice", "t1", task.StateWorking, task.StateCompleted, "done")

	// bob 应只看到自己的
	events, _, _ := svc.Pull(ctx, "bob", 0, 10)
	if len(events) != 1 || events[0].Kind != KindMessage {
		t.Fatalf("bob: %+v", events)
	}
	events, _, _ = svc.Pull(ctx, "alice", 0, 10)
	if len(events) != 2 {
		t.Fatalf("alice: %d", len(events))
	}
	// 顺序：artifact 先入（id 小），transition 后
	if events[0].Kind != KindArtifact || events[1].Kind != KindTransition {
		t.Fatalf("order: %+v", events)
	}
}

func TestService_NotifierCalled(t *testing.T) {
	svc := NewService(newMemRepo())
	var got []int64
	svc.WithNotifier(func(e *Event) { got = append(got, e.ID) })

	for i := 0; i < 3; i++ {
		_ = svc.EnqueueMessage(context.Background(), "bob", &task.Message{
			MessageID: fmt.Sprintf("m%d", i), TaskID: "t1", ContextID: "t1",
			Role: task.RoleUser, Parts: []task.Part{{Kind: task.PartText, Text: "x"}},
		})
	}
	if len(got) != 3 {
		t.Fatalf("notifier called %d times, want 3", len(got))
	}
}

func TestService_EmptyAgentRejected(t *testing.T) {
	svc := NewService(newMemRepo())
	err := svc.EnqueueMessage(context.Background(), "", &task.Message{MessageID: "m"})
	if err == nil {
		t.Fatal("empty agent should fail")
	}
}

// ─── PollWithWait 单测 ──────────────────────────────────────────

func TestPollWithWait_ImmediateReturn_WhenDataExists(t *testing.T) {
	repo := newMemRepo()
	svc := NewService(repo)
	ctx := context.Background()

	_ = svc.EnqueueMessage(ctx, "bob", &task.Message{
		MessageID: "m1", TaskID: "t1", ContextID: "t1",
		Role: task.RoleUser, Parts: []task.Part{{Kind: task.PartText, Text: "hi"}},
	})

	start := time.Now()
	events, maxID, err := svc.PollWithWait(ctx, "bob", 0, 10, 5*time.Second)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("want 1 event, got %d", len(events))
	}
	if maxID != events[0].ID {
		t.Fatalf("maxID mismatch")
	}
	if elapsed > time.Second {
		t.Fatalf("should return immediately, took %v", elapsed)
	}
}

func TestPollWithWait_WaitsAndReturns_WhenDataArrives(t *testing.T) {
	repo := newMemRepo()
	svc := NewService(repo)
	ctx := context.Background()

	go func() {
		time.Sleep(800 * time.Millisecond)
		_ = svc.EnqueueMessage(ctx, "bob", &task.Message{
			MessageID: "m1", TaskID: "t1", ContextID: "t1",
			Role: task.RoleUser, Parts: []task.Part{{Kind: task.PartText, Text: "delayed"}},
		})
	}()

	start := time.Now()
	events, _, err := svc.PollWithWait(ctx, "bob", 0, 10, 5*time.Second)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("want 1 event, got %d", len(events))
	}
	if elapsed < 500*time.Millisecond || elapsed > 3*time.Second {
		t.Fatalf("expected ~800ms-1.5s wait, got %v", elapsed)
	}
}

func TestPollWithWait_Timeout_ReturnsEmpty(t *testing.T) {
	svc := NewService(newMemRepo())
	ctx := context.Background()

	start := time.Now()
	events, maxID, err := svc.PollWithWait(ctx, "bob", 0, 10, time.Second)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Fatalf("want 0 events, got %d", len(events))
	}
	if maxID != 0 {
		t.Fatalf("maxID should be 0, got %d", maxID)
	}
	if elapsed < 900*time.Millisecond || elapsed > 2*time.Second {
		t.Fatalf("expected ~1s wait, got %v", elapsed)
	}
}

func TestPollWithWait_ZeroWait_EquivalentToPull(t *testing.T) {
	svc := NewService(newMemRepo())
	ctx := context.Background()

	start := time.Now()
	events, _, err := svc.PollWithWait(ctx, "bob", 0, 10, 0)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Fatalf("want 0, got %d", len(events))
	}
	if elapsed > 100*time.Millisecond {
		t.Fatalf("zero wait should return immediately, took %v", elapsed)
	}
}

func TestPollWithWait_ContextCancel(t *testing.T) {
	svc := NewService(newMemRepo())
	ctx, cancel := context.WithCancel(context.Background())

	go func() {
		time.Sleep(300 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	_, _, err := svc.PollWithWait(ctx, "bob", 0, 10, 10*time.Second)
	elapsed := time.Since(start)

	if err != context.Canceled {
		t.Fatalf("want context.Canceled, got %v", err)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("should cancel quickly, took %v", elapsed)
	}
}

// ─── live 集成测试 ──────────────────────────────────────────────

func liveDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("AGENT_MESH_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("AGENT_MESH_TEST_MYSQL_DSN not set")
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Ping(); err != nil {
		t.Fatal(err)
	}
	return db
}

func uniqueAgent(t *testing.T, tag string) string {
	t.Helper()
	return fmt.Sprintf("inbox-%s-%d", tag, time.Now().UnixNano())
}

func cleanupAgent(t *testing.T, db *sql.DB, agent string) {
	t.Helper()
	_, _ = db.Exec("DELETE FROM inbox_events WHERE agent_id = ?", agent)
}

func TestSQLRepo_Insert_And_ListSince(t *testing.T) {
	db := liveDB(t)
	defer db.Close()
	repo := NewSQLRepo(db)

	agent := uniqueAgent(t, "basic")
	t.Cleanup(func() { cleanupAgent(t, db, agent) })

	for i := 0; i < 3; i++ {
		_, err := repo.Insert(context.Background(), &Event{
			AgentID: agent, Kind: KindMessage, TaskID: "t1", RefID: fmt.Sprintf("m%d", i),
			Payload: json.RawMessage(`{}`),
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	// sinceID=0 拿全部
	events, err := repo.ListSince(context.Background(), agent, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 {
		t.Fatalf("got %d", len(events))
	}

	// 以第一条的 id 为 cursor，应剩 2 条
	events, err = repo.ListSince(context.Background(), agent, events[0].ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("got %d", len(events))
	}
}

func TestSQLRepo_MarkDelivered(t *testing.T) {
	db := liveDB(t)
	defer db.Close()
	repo := NewSQLRepo(db)

	agent := uniqueAgent(t, "mark")
	t.Cleanup(func() { cleanupAgent(t, db, agent) })

	e, err := repo.Insert(context.Background(), &Event{
		AgentID: agent, Kind: KindTransition, TaskID: "t1", RefID: "working",
		Payload: json.RawMessage(`{"from":"submitted","to":"working"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if e.DeliveredAt != nil {
		t.Fatal("new event should have null delivered_at")
	}

	if err := repo.MarkDelivered(context.Background(), []int64{e.ID}); err != nil {
		t.Fatal(err)
	}

	// 拉回看 delivered_at
	events, err := repo.ListSince(context.Background(), agent, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if events[0].DeliveredAt == nil {
		t.Fatal("delivered_at should be set")
	}
}

func TestSQLRepo_EmptyListByDefault(t *testing.T) {
	db := liveDB(t)
	defer db.Close()
	repo := NewSQLRepo(db)

	events, err := repo.ListSince(context.Background(), uniqueAgent(t, "empty"), 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Fatal("empty expected")
	}
}

func TestSQLRepo_EmptyAgentRejected(t *testing.T) {
	db := liveDB(t)
	defer db.Close()
	repo := NewSQLRepo(db)

	_, err := repo.Insert(context.Background(), &Event{AgentID: "", Kind: KindMessage})
	if !errors.Is(err, ErrEmptyAgent) {
		t.Fatalf("want ErrEmptyAgent, got %v", err)
	}
}
