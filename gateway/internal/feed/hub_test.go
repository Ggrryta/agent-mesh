package feed

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

func TestHub_SubscribeAndPublish_Local(t *testing.T) {
	hub := NewHub(nil, zap.NewNop())

	sub := hub.Subscribe(42)
	defer hub.Unsubscribe(sub)

	event := &FeedEvent{
		Type:    "task_message",
		AgentID: "alice",
		TaskID:  "t1",
		Payload: json.RawMessage(`{"text":"hello"}`),
	}
	hub.Publish(context.Background(), 42, event)

	select {
	case got := <-sub.Ch:
		if got.Type != "task_message" || got.AgentID != "alice" {
			t.Fatalf("unexpected event: %+v", got)
		}
		if got.Timestamp.IsZero() {
			t.Fatal("timestamp should be set")
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for event")
	}
}

func TestHub_MultipleSubscribers(t *testing.T) {
	hub := NewHub(nil, zap.NewNop())

	sub1 := hub.Subscribe(42)
	sub2 := hub.Subscribe(42)
	defer hub.Unsubscribe(sub1)
	defer hub.Unsubscribe(sub2)

	hub.Publish(context.Background(), 42, &FeedEvent{Type: "test", AgentID: "a"})

	for _, sub := range []*Subscriber{sub1, sub2} {
		select {
		case got := <-sub.Ch:
			if got.Type != "test" {
				t.Fatalf("unexpected: %+v", got)
			}
		case <-time.After(time.Second):
			t.Fatal("timeout")
		}
	}
}

func TestHub_DifferentUsers_Isolated(t *testing.T) {
	hub := NewHub(nil, zap.NewNop())

	sub1 := hub.Subscribe(1)
	sub2 := hub.Subscribe(2)
	defer hub.Unsubscribe(sub1)
	defer hub.Unsubscribe(sub2)

	hub.Publish(context.Background(), 1, &FeedEvent{Type: "for_user_1"})

	select {
	case got := <-sub1.Ch:
		if got.Type != "for_user_1" {
			t.Fatalf("unexpected: %+v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("user 1 should receive")
	}

	select {
	case got := <-sub2.Ch:
		t.Fatalf("user 2 should not receive, got: %+v", got)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestHub_Unsubscribe_StopsDelivery(t *testing.T) {
	hub := NewHub(nil, zap.NewNop())

	sub := hub.Subscribe(42)
	hub.Unsubscribe(sub)

	hub.Publish(context.Background(), 42, &FeedEvent{Type: "after_unsub"})

	select {
	case <-sub.Done():
	default:
		t.Fatal("Done channel should be closed after Unsubscribe")
	}

	select {
	case got := <-sub.Ch:
		t.Fatalf("should not receive after unsubscribe, got: %+v", got)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestHub_ChannelFull_DoesNotBlock(t *testing.T) {
	hub := NewHub(nil, zap.NewNop())
	sub := hub.Subscribe(42)
	defer hub.Unsubscribe(sub)

	// 填满 channel
	for i := 0; i < subscriberBuf+10; i++ {
		hub.Publish(context.Background(), 42, &FeedEvent{Type: "flood"})
	}

	// 不应阻塞
	if len(sub.Ch) != subscriberBuf {
		t.Fatalf("channel should be at capacity %d, got %d", subscriberBuf, len(sub.Ch))
	}
}

func TestHub_ActiveSubscribers(t *testing.T) {
	hub := NewHub(nil, zap.NewNop())

	if hub.ActiveSubscribers() != 0 {
		t.Fatal("should start at 0")
	}

	s1 := hub.Subscribe(1)
	s2 := hub.Subscribe(1)
	s3 := hub.Subscribe(2)

	if hub.ActiveSubscribers() != 3 {
		t.Fatalf("want 3, got %d", hub.ActiveSubscribers())
	}

	hub.Unsubscribe(s1)
	if hub.ActiveSubscribers() != 2 {
		t.Fatalf("want 2, got %d", hub.ActiveSubscribers())
	}

	hub.Unsubscribe(s2)
	hub.Unsubscribe(s3)
	if hub.ActiveSubscribers() != 0 {
		t.Fatalf("want 0, got %d", hub.ActiveSubscribers())
	}
}

func TestHub_HandleRedisMessage(t *testing.T) {
	hub := NewHub(nil, zap.NewNop())
	sub := hub.Subscribe(12345)
	defer hub.Unsubscribe(sub)

	event := &FeedEvent{Type: "task_transition", AgentID: "bob", TaskID: "t2"}
	data, _ := json.Marshal(event)

	hub.handleRedisMessage(&goredis.Message{
		Channel: "feed:user:12345",
		Payload: string(data),
	})

	select {
	case got := <-sub.Ch:
		if got.Type != "task_transition" || got.AgentID != "bob" {
			t.Fatalf("unexpected: %+v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout")
	}
}
