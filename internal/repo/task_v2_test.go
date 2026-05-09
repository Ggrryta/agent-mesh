package repo

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"agent-gateway/internal/model"

	"gorm.io/datatypes"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func newTaskV2TestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&model.TaskV2{}, &model.TaskMember{}, &model.TaskMessage{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func jsonParts(s string) datatypes.JSON {
	b, _ := json.Marshal([]map[string]any{{"kind": "text", "text": s}})
	return datatypes.JSON(b)
}

func TestTaskV2_CreateAndListMembers(t *testing.T) {
	db := newTaskV2TestDB(t)
	r := NewTaskV2Repo(db)
	ctx := context.Background()

	task, err := r.Create(ctx, CreateTaskParams{
		TaskID:         "t_abc",
		Title:          "review",
		CreatorAgentID: "alice",
		Members:        []string{"alice", "bob"},
		TTL:            time.Hour,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if task.Status != model.TaskV2StatusActive {
		t.Fatalf("status: %s", task.Status)
	}
	if task.ExpireAt == nil {
		t.Fatalf("expire_at not set")
	}

	members, err := r.ListMembers(ctx, "t_abc")
	if err != nil {
		t.Fatalf("ListMembers: %v", err)
	}
	if len(members) != 2 {
		t.Fatalf("expected 2 members, got %d", len(members))
	}
	byID := map[string]model.TaskMember{}
	for _, m := range members {
		byID[m.AgentID] = m
	}
	if byID["alice"].Role != model.TaskMemberRoleCreator {
		t.Fatalf("alice should be creator: %s", byID["alice"].Role)
	}
	if byID["bob"].Role != model.TaskMemberRoleMember {
		t.Fatalf("bob should be member: %s", byID["bob"].Role)
	}
}

func TestTaskV2_CreateWithInitialMessage(t *testing.T) {
	db := newTaskV2TestDB(t)
	r := NewTaskV2Repo(db)
	ctx := context.Background()

	_, err := r.Create(ctx, CreateTaskParams{
		TaskID:         "t_1",
		CreatorAgentID: "alice",
		Members:        []string{"alice", "bob"},
		InitialMessage: &InitialMessage{
			MessageID: "m_1",
			Content:   jsonParts("hello bob"),
		},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	msgs, err := r.ListMessages(ctx, "t_1", -1, 10)
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 msg, got %d", len(msgs))
	}
	if msgs[0].Seq != 0 || msgs[0].MessageID != "m_1" {
		t.Fatalf("bad msg: %+v", msgs[0])
	}
}

func TestTaskV2_AppendMessageSeqIncrement(t *testing.T) {
	db := newTaskV2TestDB(t)
	r := NewTaskV2Repo(db)
	ctx := context.Background()

	_, _ = r.Create(ctx, CreateTaskParams{
		TaskID:         "t_seq",
		CreatorAgentID: "alice",
		Members:        []string{"alice", "bob"},
	})

	for i, sender := range []string{"alice", "bob", "alice"} {
		msg, err := r.AppendMessage(ctx, "t_seq", sender, "mid_"+sender+string(rune('0'+i)),
			jsonParts("msg"+string(rune('0'+i))))
		if err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
		if msg.Seq != i {
			t.Fatalf("seq expected %d got %d", i, msg.Seq)
		}
	}

	msgs, _ := r.ListMessages(ctx, "t_seq", -1, 10)
	if len(msgs) != 3 {
		t.Fatalf("expected 3 msgs, got %d", len(msgs))
	}
}

func TestTaskV2_CloseBlocksMessages(t *testing.T) {
	db := newTaskV2TestDB(t)
	r := NewTaskV2Repo(db)
	ctx := context.Background()

	_, _ = r.Create(ctx, CreateTaskParams{
		TaskID:         "t_closed",
		CreatorAgentID: "alice",
		Members:        []string{"alice", "bob"},
	})
	_ = r.Close(ctx, "t_closed", model.TaskV2StatusClosed)

	_, err := r.AppendMessage(ctx, "t_closed", "alice", "m1", jsonParts("x"))
	if err != ErrTaskV2BadState {
		t.Fatalf("expected ErrTaskV2BadState, got %v", err)
	}
}

func TestTaskV2_ListByMember(t *testing.T) {
	db := newTaskV2TestDB(t)
	r := NewTaskV2Repo(db)
	ctx := context.Background()

	_, _ = r.Create(ctx, CreateTaskParams{TaskID: "t1", CreatorAgentID: "a", Members: []string{"a", "b"}})
	_, _ = r.Create(ctx, CreateTaskParams{TaskID: "t2", CreatorAgentID: "b", Members: []string{"b", "c"}})

	as, _ := r.ListByMember(ctx, "a", nil, 10)
	if len(as) != 1 || as[0].TaskID != "t1" {
		t.Fatalf("a should have t1 only: %+v", as)
	}
	bs, _ := r.ListByMember(ctx, "b", nil, 10)
	if len(bs) != 2 {
		t.Fatalf("b should have 2 tasks: %d", len(bs))
	}
}

func TestTaskV2_UpdateLastReadSeq(t *testing.T) {
	db := newTaskV2TestDB(t)
	r := NewTaskV2Repo(db)
	ctx := context.Background()

	_, _ = r.Create(ctx, CreateTaskParams{TaskID: "t", CreatorAgentID: "a", Members: []string{"a", "b"}})

	if err := r.UpdateLastReadSeq(ctx, "t", "b", 5); err != nil {
		t.Fatalf("update: %v", err)
	}
	members, _ := r.ListMembers(ctx, "t")
	for _, m := range members {
		if m.AgentID == "b" && m.LastReadSeq != 5 {
			t.Fatalf("b last_read expected 5 got %d", m.LastReadSeq)
		}
	}

	// 不能倒退
	_ = r.UpdateLastReadSeq(ctx, "t", "b", 3)
	members, _ = r.ListMembers(ctx, "t")
	for _, m := range members {
		if m.AgentID == "b" && m.LastReadSeq != 5 {
			t.Fatalf("b last_read should not decrease: %d", m.LastReadSeq)
		}
	}
}

func TestTaskV2_IsMember(t *testing.T) {
	db := newTaskV2TestDB(t)
	r := NewTaskV2Repo(db)
	ctx := context.Background()

	_, _ = r.Create(ctx, CreateTaskParams{TaskID: "t", CreatorAgentID: "a", Members: []string{"a", "b"}})

	ok, _ := r.IsMember(ctx, "t", "a")
	if !ok {
		t.Fatalf("a should be member")
	}
	ok, _ = r.IsMember(ctx, "t", "c")
	if ok {
		t.Fatalf("c should not be member")
	}
}
