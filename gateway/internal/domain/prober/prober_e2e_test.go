package prober

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap/zaptest"
)

// fakeAgent 是一个 stub 上游，/health 的响应可以运行时切换。
type fakeAgent struct {
	ok atomic.Bool
	sv *httptest.Server
}

func newFakeAgent() *fakeAgent {
	fa := &fakeAgent{}
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		if fa.ok.Load() {
			w.WriteHeader(200)
			return
		}
		w.WriteHeader(503)
	})
	fa.sv = httptest.NewServer(mux)
	fa.ok.Store(true)
	return fa
}

func (f *fakeAgent) URL() string { return f.sv.URL }
func (f *fakeAgent) Close()      { f.sv.Close() }

// TestProbeFlipsStatus 验证端到端行为：把 fake agent 关掉后，
// prober 在 FailureThreshold 次失败后把 status 翻成 inactive；
// 再开回去，应当恢复为 active。
func TestProbeFlipsStatus(t *testing.T) {
	db := liveDB(t)
	defer db.Close()

	fa := newFakeAgent()
	defer fa.Close()

	agentID := fmt.Sprintf("prober-flip-%d", time.Now().UnixNano())
	seedAgent(t, db, agentID, fa.URL(), "active")
	t.Cleanup(func() { cleanup(t, db, agentID) })

	// Tight intervals to keep the test sub-second.
	p := New(db, nil, Config{
		Interval:         30 * time.Millisecond,
		ClaimTTL:         10 * time.Millisecond,
		HTTPTimeout:      500 * time.Millisecond,
		FailureThreshold: 2,
	}, zaptest.NewLogger(t))

	ctx, cancel := context.WithCancel(context.Background())
	go p.Run(ctx)
	defer cancel()

	// Give the prober a tick against a healthy agent, then flip it off.
	time.Sleep(100 * time.Millisecond)
	fa.ok.Store(false)

	if !waitForStatus(t, db, agentID, "inactive", 2*time.Second) {
		t.Fatalf("status still %q after timeout", readStatus(t, db, agentID))
	}

	// Bring the agent back; status should return to active.
	fa.ok.Store(true)
	if !waitForStatus(t, db, agentID, "active", 2*time.Second) {
		t.Fatalf("recovery: status still %q", readStatus(t, db, agentID))
	}
}

func readStatus(t *testing.T, db *sql.DB, agentID string) string {
	t.Helper()
	var s string
	if err := db.QueryRow("SELECT status FROM agents WHERE agent_id = ?", agentID).Scan(&s); err != nil {
		t.Fatalf("read status: %v", err)
	}
	return s
}

func waitForStatus(t *testing.T, db *sql.DB, agentID, want string, timeout time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if readStatus(t, db, agentID) == want {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}
