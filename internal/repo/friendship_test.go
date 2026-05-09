package repo

import (
	"context"
	"testing"

	"agent-gateway/internal/model"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func newFriendshipTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&model.Friendship{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func TestFriendship_RequestAcceptIsFriend(t *testing.T) {
	db := newFriendshipTestDB(t)
	r := NewFriendshipRepo(db)
	ctx := context.Background()

	// alice 请求加 bob
	f, err := r.Request(ctx, "alice", "bob", "hi")
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	if f.AgentAID != "alice" || f.AgentBID != "bob" {
		t.Fatalf("normalize failed: a=%s b=%s", f.AgentAID, f.AgentBID)
	}
	if f.Status != model.FriendshipStatusPending || f.InitiatorID != "alice" {
		t.Fatalf("unexpected state: %+v", f)
	}

	// 加好友前不是朋友
	ok, err := r.IsFriend(ctx, "alice", "bob")
	if err != nil || ok {
		t.Fatalf("IsFriend pending should be false: ok=%v err=%v", ok, err)
	}

	// alice 不能接受自己发起的请求
	if err := r.Accept(ctx, f.ID, "alice"); err == nil {
		t.Fatalf("self-accept should fail")
	}

	// bob 接受
	if err := r.Accept(ctx, f.ID, "bob"); err != nil {
		t.Fatalf("Accept by bob: %v", err)
	}

	// 双向互为好友
	for _, pair := range [][2]string{{"alice", "bob"}, {"bob", "alice"}} {
		ok, err := r.IsFriend(ctx, pair[0], pair[1])
		if err != nil || !ok {
			t.Fatalf("IsFriend(%s,%s) should be true: ok=%v err=%v", pair[0], pair[1], ok, err)
		}
	}

	// 列表能查到
	friends, err := r.ListFriends(ctx, "alice")
	if err != nil || len(friends) != 1 {
		t.Fatalf("ListFriends alice: len=%d err=%v", len(friends), err)
	}
	if friends[0].Counterpart("alice") != "bob" {
		t.Fatalf("counterpart wrong: %s", friends[0].Counterpart("alice"))
	}
}

func TestFriendship_CannotSelfFriend(t *testing.T) {
	db := newFriendshipTestDB(t)
	r := NewFriendshipRepo(db)
	if _, err := r.Request(context.Background(), "alice", "alice", ""); err != ErrFriendshipSelf {
		t.Fatalf("expected ErrFriendshipSelf, got %v", err)
	}
}

func TestFriendship_RejectThenReRequest(t *testing.T) {
	db := newFriendshipTestDB(t)
	r := NewFriendshipRepo(db)
	ctx := context.Background()

	f, _ := r.Request(ctx, "alice", "bob", "v1")
	if err := r.Reject(ctx, f.ID, "bob"); err != nil {
		t.Fatalf("Reject: %v", err)
	}

	// 重新请求,应复用同一行,状态回到 pending
	f2, err := r.Request(ctx, "alice", "bob", "v2")
	if err != nil {
		t.Fatalf("Request again: %v", err)
	}
	if f2.ID != f.ID {
		t.Fatalf("should reuse same row: %d vs %d", f.ID, f2.ID)
	}

	// 查一下实际状态
	got, _ := r.GetByID(ctx, f.ID)
	if got.Status != model.FriendshipStatusPending || got.Reason != "v2" {
		t.Fatalf("state not updated: %+v", got)
	}
}

func TestFriendship_RevokeAfterAccept(t *testing.T) {
	db := newFriendshipTestDB(t)
	r := NewFriendshipRepo(db)
	ctx := context.Background()

	f, _ := r.Request(ctx, "alice", "bob", "")
	_ = r.Accept(ctx, f.ID, "bob")

	// alice 主动撤销
	if err := r.Revoke(ctx, f.ID, "alice"); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	ok, _ := r.IsFriend(ctx, "alice", "bob")
	if ok {
		t.Fatalf("should not be friend after revoke")
	}
}

func TestFriendship_ListPendingOnlyForReceiver(t *testing.T) {
	db := newFriendshipTestDB(t)
	r := NewFriendshipRepo(db)
	ctx := context.Background()

	_, _ = r.Request(ctx, "alice", "bob", "")
	_, _ = r.Request(ctx, "carol", "bob", "")

	// bob 看到 2 个 pending
	bobPending, _ := r.ListPending(ctx, "bob")
	if len(bobPending) != 2 {
		t.Fatalf("bob should see 2 pending, got %d", len(bobPending))
	}

	// alice 自己发起的不在 pending 列表
	alicePending, _ := r.ListPending(ctx, "alice")
	if len(alicePending) != 0 {
		t.Fatalf("alice shouldn't see her own outgoing request: %d", len(alicePending))
	}
}

func TestFriendship_NormalizePair(t *testing.T) {
	a, b := model.NormalizePair("bob", "alice")
	if a != "alice" || b != "bob" {
		t.Fatalf("normalize: %s,%s", a, b)
	}
	a, b = model.NormalizePair("x", "y")
	if a != "x" || b != "y" {
		t.Fatalf("preserve order: %s,%s", a, b)
	}
}
