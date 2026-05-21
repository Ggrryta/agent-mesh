package apikey

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

// liveDB 返回集成测试 DSN；未配置时跳过。
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

// cleanupOwner 清掉某个 owner 的全部 key，便于测试彼此独立。
func cleanupOwner(t *testing.T, db *sql.DB, ownerUID int64) {
	t.Helper()
	_, _ = db.Exec("DELETE FROM api_keys WHERE owner_uid = ?", ownerUID)
}

func TestSQLRepo_InsertAndFindByHash(t *testing.T) {
	db := liveDB(t)
	defer db.Close()
	repo := NewSQLRepo(db)

	const uid int64 = 101
	t.Cleanup(func() { cleanupOwner(t, db, uid) })

	k, err := repo.Insert(context.Background(), uid, "hash-insert-roundtrip", "sk-am_abc123456789", "ci")
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	if k.ID == 0 || k.OwnerUID != uid || k.Label != "ci" {
		t.Fatalf("unexpected row: %+v", k)
	}

	got, err := repo.FindByHash(context.Background(), "hash-insert-roundtrip")
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	if got.ID != k.ID {
		t.Fatalf("id mismatch: %d vs %d", got.ID, k.ID)
	}
	if !got.IsActive() {
		t.Fatal("fresh key should be active")
	}
}

func TestSQLRepo_FindMissingReturnsErr(t *testing.T) {
	db := liveDB(t)
	defer db.Close()
	repo := NewSQLRepo(db)

	_, err := repo.FindByHash(context.Background(), "hash-that-does-not-exist-xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx")
	if !errors.Is(err, ErrKeyNotFound) {
		t.Fatalf("want ErrKeyNotFound, got %v", err)
	}
}

func TestSQLRepo_RevokeLifecycle(t *testing.T) {
	db := liveDB(t)
	defer db.Close()
	repo := NewSQLRepo(db)

	const uid int64 = 102
	t.Cleanup(func() { cleanupOwner(t, db, uid) })

	k, err := repo.Insert(context.Background(), uid, "hash-revoke-lifecycle", "sk-am_revoke-pref", "")
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	// 首次吊销成功。
	if err := repo.Revoke(context.Background(), uid, k.ID); err != nil {
		t.Fatalf("first revoke: %v", err)
	}
	// 行已被标为吊销。
	got, err := repo.FindByHash(context.Background(), "hash-revoke-lifecycle")
	if err != nil {
		t.Fatalf("find after revoke: %v", err)
	}
	if got.IsActive() {
		t.Fatal("key should not be active after revoke")
	}
	// 再次吊销幂等。
	if err := repo.Revoke(context.Background(), uid, k.ID); err != nil {
		t.Fatalf("second revoke should be no-op, got %v", err)
	}
	// 非 owner 来吊销返回 not found。
	if err := repo.Revoke(context.Background(), 999, k.ID); !errors.Is(err, ErrKeyNotFound) {
		t.Fatalf("cross-owner revoke: want ErrKeyNotFound, got %v", err)
	}
}

func TestSQLRepo_ListByOwner(t *testing.T) {
	db := liveDB(t)
	defer db.Close()
	repo := NewSQLRepo(db)

	const uidA, uidB int64 = 201, 202
	t.Cleanup(func() { cleanupOwner(t, db, uidA); cleanupOwner(t, db, uidB) })

	_, _ = repo.Insert(context.Background(), uidA, "hash-list-a1", "sk-am_a1xxx", "a1")
	_, _ = repo.Insert(context.Background(), uidA, "hash-list-a2", "sk-am_a2xxx", "a2")
	_, _ = repo.Insert(context.Background(), uidB, "hash-list-b1", "sk-am_b1xxx", "b1")

	listA, err := repo.ListByOwner(context.Background(), uidA)
	if err != nil {
		t.Fatal(err)
	}
	if len(listA) != 2 {
		t.Fatalf("owner A: want 2, got %d", len(listA))
	}
	listB, err := repo.ListByOwner(context.Background(), uidB)
	if err != nil {
		t.Fatal(err)
	}
	if len(listB) != 1 {
		t.Fatalf("owner B: want 1, got %d", len(listB))
	}
}

func TestSQLRepo_TouchLastUsed(t *testing.T) {
	db := liveDB(t)
	defer db.Close()
	repo := NewSQLRepo(db)

	const uid int64 = 301
	t.Cleanup(func() { cleanupOwner(t, db, uid) })

	k, err := repo.Insert(context.Background(), uid, "hash-touch", "sk-am_touchxxx", "")
	if err != nil {
		t.Fatal(err)
	}
	if k.LastUsedAt != nil {
		t.Fatal("last_used_at should be NULL initially")
	}

	ts := time.Now().Truncate(time.Millisecond)
	if err := repo.TouchLastUsed(context.Background(), k.ID, ts); err != nil {
		t.Fatalf("touch: %v", err)
	}
	got, err := repo.FindByHash(context.Background(), "hash-touch")
	if err != nil {
		t.Fatal(err)
	}
	if got.LastUsedAt == nil {
		t.Fatal("last_used_at still NULL after touch")
	}
	// MySQL DATETIME(3) 截毫秒后应与 ts 同秒级（时区视 DSN 设置）。
	if got.LastUsedAt.Unix() != ts.Unix() {
		t.Fatalf("unexpected last_used_at: %v vs %v", got.LastUsedAt, ts)
	}
}
