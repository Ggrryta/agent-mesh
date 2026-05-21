package friendship

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

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

// uniqueAgentID 让每次测试用独立 agent_id，避免和历史数据 / 其他测试撞。
func uniqueAgentID(t *testing.T, tag string) string {
	return fmt.Sprintf("frtest-%s-%d", tag, time.Now().UnixNano())
}

func cleanupPair(t *testing.T, db *sql.DB, from, to string) {
	t.Helper()
	_, _ = db.Exec("DELETE FROM friendships WHERE from_agent_id IN (?, ?) OR to_agent_id IN (?, ?)",
		from, to, from, to)
}

func TestSQLRepo_InsertAndGet(t *testing.T) {
	db := liveDB(t)
	defer db.Close()
	repo := NewSQLRepo(db)

	from := uniqueAgentID(t, "a")
	to := uniqueAgentID(t, "b")
	t.Cleanup(func() { cleanupPair(t, db, from, to) })

	f, err := repo.Insert(context.Background(), from, to, "hi")
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	if f.Status != StatusPending || f.Reason != "hi" {
		t.Fatalf("unexpected: %+v", f)
	}

	got, err := repo.GetByPair(context.Background(), from, to)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.ID != f.ID {
		t.Fatalf("id mismatch: %d vs %d", got.ID, f.ID)
	}
}

func TestSQLRepo_UniqueConstraint(t *testing.T) {
	db := liveDB(t)
	defer db.Close()
	repo := NewSQLRepo(db)

	from := uniqueAgentID(t, "a")
	to := uniqueAgentID(t, "b")
	t.Cleanup(func() { cleanupPair(t, db, from, to) })

	if _, err := repo.Insert(context.Background(), from, to, ""); err != nil {
		t.Fatal(err)
	}
	// 二次 Insert 必须失败（uk_pair）
	if _, err := repo.Insert(context.Background(), from, to, ""); err == nil {
		t.Fatal("expected uk_pair violation")
	}
}

func TestSQLRepo_UpdateToPending_OnlyTerminal(t *testing.T) {
	db := liveDB(t)
	defer db.Close()
	repo := NewSQLRepo(db)

	from := uniqueAgentID(t, "a")
	to := uniqueAgentID(t, "b")
	t.Cleanup(func() { cleanupPair(t, db, from, to) })

	f, _ := repo.Insert(context.Background(), from, to, "v1")

	// pending 不应该被拉回 pending（no-op）
	updated, err := repo.UpdateToPending(context.Background(), f.ID, "v2")
	if err != nil {
		t.Fatal(err)
	}
	if updated {
		t.Fatal("pending → pending should be no-op")
	}

	// accept → rejected 路径：先 accept 再走不到这里，所以直接 reject。
	if ok, err := repo.UpdateStatus(context.Background(), f.ID, StatusPending, StatusRejected); err != nil || !ok {
		t.Fatalf("reject: %v %v", ok, err)
	}

	// 现在是 rejected，UpdateToPending 应该生效
	updated, err = repo.UpdateToPending(context.Background(), f.ID, "v2")
	if err != nil {
		t.Fatal(err)
	}
	if !updated {
		t.Fatal("rejected → pending should update")
	}
	got, _ := repo.GetByID(context.Background(), f.ID)
	if got.Status != StatusPending || got.Reason != "v2" {
		t.Fatalf("unexpected: %+v", got)
	}
}

func TestSQLRepo_UpdateStatus_RequiresExactFromState(t *testing.T) {
	db := liveDB(t)
	defer db.Close()
	repo := NewSQLRepo(db)

	from := uniqueAgentID(t, "a")
	to := uniqueAgentID(t, "b")
	t.Cleanup(func() { cleanupPair(t, db, from, to) })

	f, _ := repo.Insert(context.Background(), from, to, "")
	// 先 accept 一次
	if ok, _ := repo.UpdateStatus(context.Background(), f.ID, StatusPending, StatusAccepted); !ok {
		t.Fatal("first transition should win")
	}
	// 再次 pending→accepted 必须 no-op，因为状态已经是 accepted
	if ok, _ := repo.UpdateStatus(context.Background(), f.ID, StatusPending, StatusAccepted); ok {
		t.Fatal("second transition on accepted row must be no-op")
	}
}

func TestSQLRepo_ListInvolving(t *testing.T) {
	db := liveDB(t)
	defer db.Close()
	repo := NewSQLRepo(db)

	a := uniqueAgentID(t, "a")
	b := uniqueAgentID(t, "b")
	c := uniqueAgentID(t, "c")
	t.Cleanup(func() { cleanupPair(t, db, a, b); cleanupPair(t, db, a, c); cleanupPair(t, db, b, c) })

	_, _ = repo.Insert(context.Background(), a, b, "")
	_, _ = repo.Insert(context.Background(), c, a, "")
	_, _ = repo.Insert(context.Background(), b, c, "")

	aList, err := repo.ListInvolvingAgent(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	if len(aList) != 2 {
		t.Fatalf("a involved in 2 rows, got %d", len(aList))
	}
	for _, f := range aList {
		if !f.Involves(a) {
			t.Fatalf("row %d does not involve %s", f.ID, a)
		}
	}
}

func TestSQLRepo_ListIncomingPending(t *testing.T) {
	db := liveDB(t)
	defer db.Close()
	repo := NewSQLRepo(db)

	a := uniqueAgentID(t, "a")
	b := uniqueAgentID(t, "b")
	c := uniqueAgentID(t, "c")
	t.Cleanup(func() { cleanupPair(t, db, a, b); cleanupPair(t, db, c, b) })

	// 两个对 b 的 pending 请求，一个对 b 的 accepted 请求
	_, _ = repo.Insert(context.Background(), a, b, "req1")
	_, _ = repo.Insert(context.Background(), c, b, "req2")
	f3, _ := repo.Insert(context.Background(), b, a, "b→a, not incoming for b")
	_ = f3

	list, err := repo.ListIncomingPending(context.Background(), b)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("want 2 incoming pending for b, got %d", len(list))
	}
}

func TestSQLRepo_ExistsAccepted(t *testing.T) {
	db := liveDB(t)
	defer db.Close()
	repo := NewSQLRepo(db)

	a := uniqueAgentID(t, "a")
	b := uniqueAgentID(t, "b")
	t.Cleanup(func() { cleanupPair(t, db, a, b) })

	f, _ := repo.Insert(context.Background(), a, b, "")
	ok, err := repo.ExistsAccepted(context.Background(), a, b)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("pending should not count")
	}

	_, _ = repo.UpdateStatus(context.Background(), f.ID, StatusPending, StatusAccepted)
	ok, err = repo.ExistsAccepted(context.Background(), a, b)
	if err != nil || !ok {
		t.Fatalf("accepted a→b: %v %v", ok, err)
	}
	// 反向查询也应该 true
	ok, _ = repo.ExistsAccepted(context.Background(), b, a)
	if !ok {
		t.Fatal("exists should be symmetric")
	}
}

func TestSQLRepo_GetByPair_NotFound(t *testing.T) {
	db := liveDB(t)
	defer db.Close()
	repo := NewSQLRepo(db)
	_, err := repo.GetByPair(context.Background(),
		uniqueAgentID(t, "no"), uniqueAgentID(t, "body"))
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}
