package agent

import (
	"context"
	"database/sql"
	"encoding/json"
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

func cleanup(t *testing.T, db *sql.DB, id string) {
	t.Helper()
	_, _ = db.Exec("DELETE FROM agents WHERE agent_id = ?", id)
}

func uniqueID(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
}

func mustAgent(id string) *Agent {
	return &Agent{
		AgentID:  id,
		OwnerUID: 1,
		Name:     id,
		URL:      "http://" + id,
		Version:  "1.0",
		Kind:     KindNormal,
		Status:   StatusActive,
	}
}

func TestSQLRepo_CreateAndGet(t *testing.T) {
	db := liveDB(t)
	defer db.Close()
	repo := NewSQLRepo(db)

	id := uniqueID("cr")
	cleanup(t, db, id)
	t.Cleanup(func() { cleanup(t, db, id) })

	ctx := context.Background()
	a := mustAgent(id)
	a.AgentCardJSON = json.RawMessage(`{"streaming":true}`)
	now := time.Now().UTC().Truncate(time.Millisecond)
	a.LastHeartbeatAt = &now

	if err := repo.Create(ctx, a); err != nil {
		t.Fatalf("create: %v", err)
	}

	got, err := repo.GetByAgentID(ctx, id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.AgentID != id || got.OwnerUID != 1 || got.Kind != KindNormal || got.Status != StatusActive {
		t.Fatalf("mismatch: %+v", got)
	}
	if len(got.AgentCardJSON) == 0 {
		t.Fatalf("agent card should round-trip, got empty")
	}
	if got.LastHeartbeatAt == nil {
		t.Fatalf("heartbeat should round-trip, got nil")
	}
}

func TestSQLRepo_Create_DuplicateRejected(t *testing.T) {
	db := liveDB(t)
	defer db.Close()
	repo := NewSQLRepo(db)

	id := uniqueID("dup")
	cleanup(t, db, id)
	t.Cleanup(func() { cleanup(t, db, id) })

	ctx := context.Background()
	if err := repo.Create(ctx, mustAgent(id)); err != nil {
		t.Fatal(err)
	}
	if err := repo.Create(ctx, mustAgent(id)); !errors.Is(err, ErrAgentIDExists) {
		t.Fatalf("want ErrAgentIDExists, got %v", err)
	}
}

func TestSQLRepo_Upsert_PreservesOwner(t *testing.T) {
	db := liveDB(t)
	defer db.Close()
	repo := NewSQLRepo(db)

	id := uniqueID("up")
	cleanup(t, db, id)
	t.Cleanup(func() { cleanup(t, db, id) })

	ctx := context.Background()
	// Initial row owned by uid=1.
	if err := repo.Create(ctx, mustAgent(id)); err != nil {
		t.Fatal(err)
	}
	// Upsert with a different owner_uid must NOT steal ownership.
	evil := mustAgent(id)
	evil.OwnerUID = 999
	evil.Name = "changed"
	if err := repo.Upsert(ctx, evil); err != nil {
		t.Fatal(err)
	}
	got, err := repo.GetByAgentID(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.OwnerUID != 1 {
		t.Fatalf("owner drifted to %d", got.OwnerUID)
	}
	if got.Name != "changed" {
		t.Fatalf("name should have been upserted, got %q", got.Name)
	}
}

func TestSQLRepo_UpdateStatusAndHeartbeat(t *testing.T) {
	db := liveDB(t)
	defer db.Close()
	repo := NewSQLRepo(db)

	id := uniqueID("hb")
	cleanup(t, db, id)
	t.Cleanup(func() { cleanup(t, db, id) })

	ctx := context.Background()
	if err := repo.Create(ctx, mustAgent(id)); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpdateStatus(ctx, id, StatusInactive); err != nil {
		t.Fatal(err)
	}
	got, _ := repo.GetByAgentID(ctx, id)
	if got.Status != StatusInactive {
		t.Fatalf("want inactive, got %s", got.Status)
	}
	// Heartbeat should flip inactive back to active.
	if err := repo.UpdateHeartbeat(ctx, id, time.Now()); err != nil {
		t.Fatal(err)
	}
	got, _ = repo.GetByAgentID(ctx, id)
	if got.Status != StatusActive {
		t.Fatalf("heartbeat should revive inactive, got %s", got.Status)
	}
	if got.LastHeartbeatAt == nil {
		t.Fatalf("heartbeat ts not recorded")
	}
}

func TestSQLRepo_UpdateHeartbeat_LeavesDrainingAlone(t *testing.T) {
	db := liveDB(t)
	defer db.Close()
	repo := NewSQLRepo(db)

	id := uniqueID("drn")
	cleanup(t, db, id)
	t.Cleanup(func() { cleanup(t, db, id) })

	ctx := context.Background()
	if err := repo.Create(ctx, mustAgent(id)); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpdateStatus(ctx, id, StatusDraining); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpdateHeartbeat(ctx, id, time.Now()); err != nil {
		t.Fatal(err)
	}
	got, _ := repo.GetByAgentID(ctx, id)
	if got.Status != StatusDraining {
		t.Fatalf("heartbeat must not un-drain, got %s", got.Status)
	}
}

func TestSQLRepo_Delete(t *testing.T) {
	db := liveDB(t)
	defer db.Close()
	repo := NewSQLRepo(db)

	id := uniqueID("del")
	cleanup(t, db, id)

	ctx := context.Background()
	if err := repo.Create(ctx, mustAgent(id)); err != nil {
		t.Fatal(err)
	}
	if err := repo.Delete(ctx, id); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := repo.Delete(ctx, id); !errors.Is(err, ErrAgentNotFound) {
		t.Fatalf("second delete should be ErrAgentNotFound, got %v", err)
	}
}

func TestSQLRepo_ListFilters(t *testing.T) {
	db := liveDB(t)
	defer db.Close()
	repo := NewSQLRepo(db)

	ids := []string{uniqueID("lst-a"), uniqueID("lst-b"), uniqueID("lst-c")}
	t.Cleanup(func() {
		for _, id := range ids {
			cleanup(t, db, id)
		}
	})
	ctx := context.Background()

	// 2 active + 1 draining, all owner=1 for a self-contained query.
	for i, id := range ids {
		a := mustAgent(id)
		if i == 2 {
			a.Status = StatusDraining
		}
		if err := repo.Create(ctx, a); err != nil {
			t.Fatal(err)
		}
	}

	// Filter by status.
	active, err := repo.List(ctx, Filter{OwnerUID: 1, Status: StatusActive})
	if err != nil {
		t.Fatal(err)
	}
	seenActive := 0
	for _, a := range active {
		for _, id := range ids[:2] {
			if a.AgentID == id {
				seenActive++
			}
		}
	}
	if seenActive != 2 {
		t.Fatalf("expected 2 active matches, got %d", seenActive)
	}

	// Filter by draining.
	drain, _ := repo.List(ctx, Filter{OwnerUID: 1, Status: StatusDraining})
	found := false
	for _, a := range drain {
		if a.AgentID == ids[2] {
			found = true
		}
	}
	if !found {
		t.Fatal("draining filter did not return the draining row")
	}
}
