package user

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"testing"

	_ "github.com/go-sql-driver/mysql"
)

// liveDB 返回 dev DSN；未配置时跳过测试。
func liveDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("AGENT_MESH_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("AGENT_MESH_TEST_MYSQL_DSN not set; skipping SQLRepo tests")
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

func unique(t *testing.T, prefix string) string {
	return fmt.Sprintf("%s-%s", prefix, t.Name())
}

// cleanup 清理一个 user 及其配对的 virtual-agent。即使 Create 半路失败，
// 从 t.Cleanup 里调也是安全的。
func cleanup(t *testing.T, db *sql.DB, username string) {
	t.Helper()
	ctx := context.Background()
	_, _ = db.ExecContext(ctx,
		"DELETE FROM agents WHERE owner_uid IN (SELECT id FROM users WHERE username = ?)",
		username)
	_, _ = db.ExecContext(ctx, "DELETE FROM users WHERE username = ?", username)
}

func TestSQLRepo_CreateAndFetch(t *testing.T) {
	db := liveDB(t)
	defer db.Close()
	repo := NewSQLRepo(db)

	username := unique(t, "u")
	// Username has '/' from t.Name() in subtests; strip to match validator.
	// Tests that call this use only the top-level name so this is fine.
	cleanup(t, db, username)
	t.Cleanup(func() { cleanup(t, db, username) })

	ctx := context.Background()
	u, err := repo.CreateWithVirtualAgent(ctx, username, "$2a$10$fakehash")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if u.ID == 0 {
		t.Fatal("id is zero")
	}
	expectedVirt := VirtualAgentIDFor(u.ID)
	if u.VirtualUserAgentID != expectedVirt {
		t.Fatalf("virtual id %q != %q", u.VirtualUserAgentID, expectedVirt)
	}

	// GetByUsername
	got, err := repo.GetByUsername(ctx, username)
	if err != nil {
		t.Fatalf("GetByUsername: %v", err)
	}
	if got.ID != u.ID {
		t.Fatalf("id mismatch %d vs %d", got.ID, u.ID)
	}
	if got.PasswordHash != "$2a$10$fakehash" {
		t.Fatalf("hash round-trip failed: %q", got.PasswordHash)
	}

	// GetByID
	got2, err := repo.GetByID(ctx, u.ID)
	if err != nil || got2.Username != username {
		t.Fatalf("GetByID: %v %+v", err, got2)
	}

	// Verify the paired agents row exists with the right kind/status.
	var kind, statusStr string
	var ownerUID int64
	if err := db.QueryRowContext(ctx,
		"SELECT kind, status, owner_uid FROM agents WHERE agent_id = ?",
		expectedVirt,
	).Scan(&kind, &statusStr, &ownerUID); err != nil {
		t.Fatalf("virtual agent row: %v", err)
	}
	if kind != "virtual-user" || statusStr != "active" || ownerUID != u.ID {
		t.Fatalf("virtual agent wrong: kind=%s status=%s owner=%d", kind, statusStr, ownerUID)
	}
}

func TestSQLRepo_DuplicateUsername(t *testing.T) {
	db := liveDB(t)
	defer db.Close()
	repo := NewSQLRepo(db)

	username := unique(t, "dup")
	cleanup(t, db, username)
	t.Cleanup(func() { cleanup(t, db, username) })

	ctx := context.Background()
	_, err := repo.CreateWithVirtualAgent(ctx, username, "hash1")
	if err != nil {
		t.Fatalf("first create: %v", err)
	}
	_, err = repo.CreateWithVirtualAgent(ctx, username, "hash2")
	if !errors.Is(err, ErrUsernameTaken) {
		t.Fatalf("want ErrUsernameTaken, got %v", err)
	}
}

func TestSQLRepo_GetByUsername_NotFound(t *testing.T) {
	db := liveDB(t)
	defer db.Close()
	repo := NewSQLRepo(db)
	_, err := repo.GetByUsername(context.Background(), "never-exists-"+t.Name())
	if !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("want ErrUserNotFound, got %v", err)
	}
}
