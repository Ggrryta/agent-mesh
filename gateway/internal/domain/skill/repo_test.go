package skill

import (
	"context"
	"database/sql"
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

// seedAgent 提前插一条 parent 行。skills 表没有对 agents 的 FK，
// 但写测试时保留 parent 行让意图更清晰。
func seedAgent(t *testing.T, db *sql.DB, id string) {
	t.Helper()
	if _, err := db.Exec(`
		INSERT INTO agents (agent_id, owner_uid, name, kind, status)
		VALUES (?, 1, ?, 'normal', 'active')
		ON DUPLICATE KEY UPDATE name=VALUES(name)`, id, id); err != nil {
		t.Fatal(err)
	}
}

func cleanupSkill(t *testing.T, db *sql.DB, agentID string) {
	t.Helper()
	_, _ = db.Exec("DELETE FROM skills WHERE agent_id = ?", agentID)
	_, _ = db.Exec("DELETE FROM agents WHERE agent_id = ?", agentID)
}

func TestSQLRepo_ReplaceByAgentID_FullLifecycle(t *testing.T) {
	db := liveDB(t)
	defer db.Close()
	repo := NewSQLRepo(db)

	agentID := fmt.Sprintf("skill-t-%d", time.Now().UnixNano())
	seedAgent(t, db, agentID)
	t.Cleanup(func() { cleanupSkill(t, db, agentID) })

	ctx := context.Background()

	// First pass: two skills.
	first := []Input{
		{SkillID: "echo", Name: "Echo", Description: "echo", Tags: []string{"debug"}},
		{SkillID: "summarize", Name: "Summarize"},
	}
	if err := repo.ReplaceByAgentID(ctx, agentID, first); err != nil {
		t.Fatalf("first replace: %v", err)
	}
	got, err := repo.ListByAgentID(ctx, agentID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 skills, got %d", len(got))
	}

	// Second pass: only one skill; old ones must disappear (DELETE + INSERT
	// semantics). This is the critical invariant for mesh freshness — if it
	// broke, agents would keep advertising stale skills forever.
	second := []Input{{SkillID: "translate", Name: "Translate"}}
	if err := repo.ReplaceByAgentID(ctx, agentID, second); err != nil {
		t.Fatalf("second replace: %v", err)
	}
	got, err = repo.ListByAgentID(ctx, agentID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].SkillID != "translate" {
		t.Fatalf("old skills not wiped: %+v", got)
	}

	// Third pass: empty. The agent should have zero skills but still be a
	// valid caller (the row still exists via seedAgent).
	if err := repo.ReplaceByAgentID(ctx, agentID, nil); err != nil {
		t.Fatalf("empty replace: %v", err)
	}
	got, _ = repo.ListByAgentID(ctx, agentID)
	if len(got) != 0 {
		t.Fatalf("want 0 skills, got %d", len(got))
	}
}

func TestSQLRepo_ListByAgentID_Empty(t *testing.T) {
	db := liveDB(t)
	defer db.Close()
	repo := NewSQLRepo(db)
	got, err := repo.ListByAgentID(context.Background(), "does-not-exist")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("want empty, got %+v", got)
	}
}

func TestSQLRepo_JSONFieldsRoundTrip(t *testing.T) {
	db := liveDB(t)
	defer db.Close()
	repo := NewSQLRepo(db)

	agentID := fmt.Sprintf("skill-j-%d", time.Now().UnixNano())
	seedAgent(t, db, agentID)
	t.Cleanup(func() { cleanupSkill(t, db, agentID) })

	want := Input{
		SkillID:     "echo",
		Name:        "Echo",
		Tags:        []string{"debug", "dev", "ops"},
		InputModes:  []string{"text"},
		OutputModes: []string{"text", "json"},
	}
	if err := repo.ReplaceByAgentID(context.Background(), agentID, []Input{want}); err != nil {
		t.Fatal(err)
	}
	got, err := repo.ListByAgentID(context.Background(), agentID)
	if err != nil || len(got) != 1 {
		t.Fatalf("list: %v %+v", err, got)
	}
	s := got[0]
	if len(s.Tags) != 3 || s.Tags[2] != "ops" {
		t.Errorf("tags drift: %+v", s.Tags)
	}
	if len(s.InputModes) != 1 || s.InputModes[0] != "text" {
		t.Errorf("input_modes drift: %+v", s.InputModes)
	}
	if len(s.OutputModes) != 2 || s.OutputModes[1] != "json" {
		t.Errorf("output_modes drift: %+v", s.OutputModes)
	}
}
