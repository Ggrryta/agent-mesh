package service

import (
	"context"
	"testing"
	"time"

	"agent-gateway/internal/model"
	"agent-gateway/internal/repo"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func setupDispatcherTest(t *testing.T) (*AgentDispatcher, *repo.AgentRepo, *repo.FriendshipRepo, *repo.TaskV2Repo, *OnlineRegistry, *InboxHub) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("sqlite: %v", err)
	}
	if err := db.AutoMigrate(&model.Agent{}, &model.Friendship{},
		&model.TaskV2{}, &model.TaskMember{}, &model.TaskMessage{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	t.Cleanup(mr.Close)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})

	agentRepo := repo.NewAgentRepo(db)
	friendshipRepo := repo.NewFriendshipRepo(db)
	taskRepo := repo.NewTaskV2Repo(db)
	online := NewOnlineRegistry(rdb, 30*time.Second)
	hub := NewInboxHub()
	d := NewAgentDispatcher(agentRepo, friendshipRepo, taskRepo, online, hub, nil)
	return d, agentRepo, friendshipRepo, taskRepo, online, hub
}

func createAgent(t *testing.T, r *repo.AgentRepo, id, owner string, mode model.DeliveryMode) {
	t.Helper()
	a := &model.Agent{
		AgentID:      id,
		Name:         id,
		OwnerAppID:   owner,
		Status:       model.AgentStatusActive,
		DeliveryMode: mode,
	}
	if err := r.Create(context.Background(), a); err != nil {
		t.Fatalf("Create %s: %v", id, err)
	}
}

func becomeFriends(t *testing.T, r *repo.FriendshipRepo, a, b string) {
	t.Helper()
	f, err := r.Request(context.Background(), a, b, "")
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	if err := r.Accept(context.Background(), f.ID, b); err != nil {
		t.Fatalf("Accept: %v", err)
	}
}

func TestDispatcher_SendMessage_HappyPath(t *testing.T) {
	d, aRepo, fRepo, _, online, hub := setupDispatcherTest(t)
	ctx := context.Background()

	createAgent(t, aRepo, "alice", "app1", model.DeliveryModePull)
	createAgent(t, aRepo, "bob", "app2", model.DeliveryModePull)
	becomeFriends(t, fRepo, "alice", "bob")
	_ = online.Online(ctx, AgentOnlineInfo{AgentID: "bob", GASInstanceID: "gas-b"})

	s := hub.Subscribe("bob")
	defer hub.Unsubscribe("bob", s)

	result, err := d.SendMessage(ctx, SendMessageInput{
		Sender:    "alice",
		Target:    "bob",
		Title:     "review",
		MessageID: "msg_1",
		Parts:     []A2AMessagePart{{Kind: "text", Text: "hello"}},
	})
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if !result.IsNewTask || result.Seq != 0 {
		t.Fatalf("bad result: %+v", result)
	}

	// bob 的 inbox 能收到两个事件(task_created + task_message)
	got := 0
	for i := 0; i < 2; i++ {
		select {
		case <-s.Events:
			got++
		case <-time.After(time.Second):
			t.Fatalf("only got %d events", got)
		}
	}
}

func TestDispatcher_SendMessage_NotFriend(t *testing.T) {
	d, aRepo, _, _, _, _ := setupDispatcherTest(t)
	ctx := context.Background()

	createAgent(t, aRepo, "alice", "app1", model.DeliveryModePull)
	createAgent(t, aRepo, "bob", "app2", model.DeliveryModePull)

	_, err := d.SendMessage(ctx, SendMessageInput{
		Sender: "alice", Target: "bob", MessageID: "m1",
		Parts: []A2AMessagePart{{Kind: "text", Text: "x"}},
	})
	if err != ErrNotFriend {
		t.Fatalf("expected ErrNotFriend, got %v", err)
	}
}

func TestDispatcher_SendMessage_AgentOffline(t *testing.T) {
	d, aRepo, fRepo, _, _, _ := setupDispatcherTest(t)
	ctx := context.Background()

	createAgent(t, aRepo, "alice", "app1", model.DeliveryModePull)
	createAgent(t, aRepo, "bob", "app2", model.DeliveryModePull)
	becomeFriends(t, fRepo, "alice", "bob")
	// bob 没上线

	_, err := d.SendMessage(ctx, SendMessageInput{
		Sender: "alice", Target: "bob", MessageID: "m1",
		Parts: []A2AMessagePart{{Kind: "text", Text: "x"}},
	})
	if err != ErrAgentOffline {
		t.Fatalf("expected ErrAgentOffline, got %v", err)
	}
}

func TestDispatcher_SendMessage_AppendToExistingTask(t *testing.T) {
	d, aRepo, fRepo, taskRepo, online, _ := setupDispatcherTest(t)
	ctx := context.Background()

	createAgent(t, aRepo, "alice", "app1", model.DeliveryModePull)
	createAgent(t, aRepo, "bob", "app2", model.DeliveryModePull)
	becomeFriends(t, fRepo, "alice", "bob")
	_ = online.Online(ctx, AgentOnlineInfo{AgentID: "alice", GASInstanceID: "gas-a"})
	_ = online.Online(ctx, AgentOnlineInfo{AgentID: "bob", GASInstanceID: "gas-b"})

	// alice 发起
	r1, _ := d.SendMessage(ctx, SendMessageInput{
		Sender: "alice", Target: "bob", MessageID: "m1",
		Parts: []A2AMessagePart{{Kind: "text", Text: "hi"}},
	})

	// bob 回复(追加消息)
	r2, err := d.SendMessage(ctx, SendMessageInput{
		Sender: "bob", Target: "alice", TaskID: r1.TaskID, MessageID: "m2",
		Parts: []A2AMessagePart{{Kind: "text", Text: "hello"}},
	})
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	if r2.Seq != 1 || r2.IsNewTask {
		t.Fatalf("bad append: %+v", r2)
	}

	msgs, _ := taskRepo.ListMessages(ctx, r1.TaskID, -1, 10)
	if len(msgs) != 2 {
		t.Fatalf("expected 2 msgs, got %d", len(msgs))
	}
}

func TestDispatcher_CloseTask(t *testing.T) {
	d, aRepo, fRepo, taskRepo, online, hub := setupDispatcherTest(t)
	ctx := context.Background()

	createAgent(t, aRepo, "alice", "app1", model.DeliveryModePull)
	createAgent(t, aRepo, "bob", "app2", model.DeliveryModePull)
	becomeFriends(t, fRepo, "alice", "bob")
	_ = online.Online(ctx, AgentOnlineInfo{AgentID: "bob", GASInstanceID: "gas-b"})

	r, _ := d.SendMessage(ctx, SendMessageInput{
		Sender: "alice", Target: "bob", MessageID: "m1",
		Parts: []A2AMessagePart{{Kind: "text", Text: "x"}},
	})

	// alice 订阅 inbox,等 bob 关闭的通知
	s := hub.Subscribe("alice")
	defer hub.Unsubscribe("alice", s)

	if err := d.CloseTask(ctx, r.TaskID, "bob"); err != nil {
		t.Fatalf("CloseTask: %v", err)
	}

	t2, _ := taskRepo.Get(ctx, r.TaskID)
	if t2.Status != model.TaskV2StatusClosed {
		t.Fatalf("status: %s", t2.Status)
	}

	// alice 应收到 task_closed 事件
	select {
	case evt := <-s.Events:
		if evt.Kind != InboxEventTaskClosed {
			t.Fatalf("wrong event: %s", evt.Kind)
		}
	case <-time.After(time.Second):
		t.Fatalf("no close event")
	}
}
