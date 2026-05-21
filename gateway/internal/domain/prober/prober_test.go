package prober

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"go.uber.org/zap/zaptest"
)

// liveDB 返回集成测试 DSN；未配置时跳过测试。
func liveDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("AGENT_MESH_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("AGENT_MESH_TEST_MYSQL_DSN not set")
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := db.Ping(); err != nil {
		t.Fatalf("ping: %v", err)
	}
	return db
}

func seedAgent(t *testing.T, db *sql.DB, id, url, status string) {
	t.Helper()
	if _, err := db.Exec(`
		INSERT INTO agents (agent_id, owner_uid, name, url, kind, status)
		VALUES (?, 1, ?, ?, 'normal', ?)
		ON DUPLICATE KEY UPDATE url=VALUES(url), status=VALUES(status), last_probed_at=NULL`,
		id, id, url, status); err != nil {
		t.Fatalf("seed: %v", err)
	}
}

func cleanup(t *testing.T, db *sql.DB, id string) {
	t.Helper()
	_, _ = db.Exec("DELETE FROM agents WHERE agent_id = ?", id)
}

// TestClaim_IsAtomicAcrossReplicas 验证并发契约：
// N 个 goroutine 争抢同一行时，恰好一个能赢。
func TestClaim_IsAtomicAcrossReplicas(t *testing.T) {
	db := liveDB(t)
	defer db.Close()

	agentID := fmt.Sprintf("prober-race-%d", time.Now().UnixNano())
	seedAgent(t, db, agentID, "http://example.invalid", "active")
	t.Cleanup(func() { cleanup(t, db, agentID) })

	p := New(db, nil, Config{ClaimTTL: 15 * time.Second}, zaptest.NewLogger(t))

	var winners atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := p.claim(context.Background(), agentID)
			if err != nil {
				t.Errorf("claim: %v", err)
				return
			}
			if ok {
				winners.Add(1)
			}
		}()
	}
	wg.Wait()
	if got := winners.Load(); got != 1 {
		t.Fatalf("want exactly 1 winner, got %d", got)
	}
}

// TestClaim_RespectsTTL：TTL 窗口内重复 claim 必须全部失败；
// TTL 过了之后另一个副本才能再赢一次。
func TestClaim_RespectsTTL(t *testing.T) {
	db := liveDB(t)
	defer db.Close()

	agentID := fmt.Sprintf("prober-ttl-%d", time.Now().UnixNano())
	seedAgent(t, db, agentID, "http://example.invalid", "active")
	t.Cleanup(func() { cleanup(t, db, agentID) })

	// TTL 调小，让测试跑得快。
	p := New(db, nil, Config{ClaimTTL: 500 * time.Millisecond}, zaptest.NewLogger(t))

	ctx := context.Background()
	first, err := p.claim(ctx, agentID)
	if err != nil || !first {
		t.Fatalf("first claim: %v %v", first, err)
	}
	secondRapid, err := p.claim(ctx, agentID)
	if err != nil {
		t.Fatal(err)
	}
	if secondRapid {
		t.Fatal("second claim within TTL should fail")
	}

	time.Sleep(600 * time.Millisecond)
	third, err := p.claim(ctx, agentID)
	if err != nil || !third {
		t.Fatalf("third claim after TTL expiry: %v %v", third, err)
	}
}

func TestListCandidates_SkipsVirtualAndNoURL(t *testing.T) {
	db := liveDB(t)
	defer db.Close()

	hasURL := fmt.Sprintf("prober-withurl-%d", time.Now().UnixNano())
	noURL := fmt.Sprintf("prober-nourl-%d", time.Now().UnixNano())
	virt := fmt.Sprintf("virtual-user-99999")

	seedAgent(t, db, hasURL, "http://somewhere", "active")
	if _, err := db.Exec(`
		INSERT INTO agents (agent_id, owner_uid, name, url, kind, status)
		VALUES (?, 1, ?, '', 'normal', 'active')
		ON DUPLICATE KEY UPDATE url=''`,
		noURL, noURL); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO agents (agent_id, owner_uid, name, url, kind, status)
		VALUES (?, 1, ?, 'http://virtual', 'virtual-user', 'active')
		ON DUPLICATE KEY UPDATE url='http://virtual'`,
		virt, virt); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup(t, db, hasURL)
		cleanup(t, db, noURL)
		cleanup(t, db, virt)
	})

	p := New(db, nil, Config{}, zaptest.NewLogger(t))
	cs, err := p.listCandidates(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, c := range cs {
		ids[c.agentID] = true
	}
	if !ids[hasURL] {
		t.Errorf("should include %s", hasURL)
	}
	if ids[noURL] {
		t.Errorf("should NOT include empty-URL %s", noURL)
	}
	if ids[virt] {
		t.Errorf("should NOT include virtual-user %s", virt)
	}
}
