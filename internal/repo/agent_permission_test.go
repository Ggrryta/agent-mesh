package repo

import (
	"context"
	"testing"
	"time"

	"agent-gateway/internal/model"
	"agent-gateway/pkg/cache"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type stubPermissionBlacklist struct {
	keys map[string]bool
}

func newStubPermissionBlacklist() *stubPermissionBlacklist {
	return &stubPermissionBlacklist{keys: make(map[string]bool)}
}

func (s *stubPermissionBlacklist) Exists(_ context.Context, key string) (bool, error) {
	return s.keys[key], nil
}

func (s *stubPermissionBlacklist) Set(_ context.Context, key string, _ time.Duration) error {
	s.keys[key] = true
	return nil
}

func (s *stubPermissionBlacklist) Delete(_ context.Context, key string) error {
	delete(s.keys, key)
	return nil
}

func newPermissionTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite failed: %v", err)
	}
	if err := db.AutoMigrate(&model.AgentPermission{}, &model.AgentApply{}); err != nil {
		t.Fatalf("migrate failed: %v", err)
	}
	return db
}

func TestAgentPermissionRepo_GrantClearsRevokeBlacklist(t *testing.T) {
	ctx := context.Background()
	blacklist := newStubPermissionBlacklist()
	key := revokeBlacklistKey("agent-1", "consumer-1")
	blacklist.keys[key] = true

	repo := &AgentPermissionRepo{
		db:        newPermissionTestDB(t),
		blacklist: blacklist,
		cache:     cache.New[string, bool](time.Minute),
	}

	if err := repo.Grant(ctx, "agent-1", "owner-1", "consumer-1"); err != nil {
		t.Fatalf("grant failed: %v", err)
	}
	if blacklist.keys[key] {
		t.Fatalf("expected revoke blacklist cleared after grant")
	}

	has, err := repo.HasPermission(ctx, "agent-1", "consumer-1")
	if err != nil {
		t.Fatalf("HasPermission failed: %v", err)
	}
	if !has {
		t.Fatalf("expected permission to be visible after grant")
	}
}

func TestAgentApplyRepo_ApproveClearsRevokeBlacklist(t *testing.T) {
	ctx := context.Background()
	db := newPermissionTestDB(t)
	blacklist := newStubPermissionBlacklist()
	key := revokeBlacklistKey("agent-1", "consumer-1")
	blacklist.keys[key] = true

	applyRepo := &AgentApplyRepo{
		db:        db,
		blacklist: blacklist,
	}

	apply := &model.AgentApply{
		AgentID:        "agent-1",
		OwnerAppID:     "owner-1",
		ApplicantAppID: "consumer-1",
		Status:         model.ApplyStatusPending,
	}
	if err := db.Create(apply).Error; err != nil {
		t.Fatalf("create apply failed: %v", err)
	}

	if err := applyRepo.Approve(ctx, apply.ID, "agent-1", "owner-1", "consumer-1"); err != nil {
		t.Fatalf("approve failed: %v", err)
	}
	if blacklist.keys[key] {
		t.Fatalf("expected revoke blacklist cleared after approve")
	}

	var perm model.AgentPermission
	if err := db.Where("agent_id = ? AND consumer_app_id = ?", "agent-1", "consumer-1").First(&perm).Error; err != nil {
		t.Fatalf("expected permission row, got error: %v", err)
	}
}
