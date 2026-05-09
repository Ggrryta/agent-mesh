package service

import (
	"context"
	"testing"
	"time"
)

func TestInboxHub_SubscribePublish(t *testing.T) {
	h := NewInboxHub()
	s := h.Subscribe("alice")
	defer h.Unsubscribe("alice", s)

	ok := h.Publish("alice", InboxEventTaskMessage, map[string]any{"hi": 1})
	if !ok {
		t.Fatalf("Publish should succeed for subscribed agent")
	}

	select {
	case evt := <-s.Events:
		if evt.Kind != InboxEventTaskMessage {
			t.Fatalf("wrong kind: %s", evt.Kind)
		}
	case <-time.After(time.Second):
		t.Fatalf("no event delivered")
	}
}

func TestInboxHub_PublishOffline(t *testing.T) {
	h := NewInboxHub()
	if ok := h.Publish("nobody", InboxEventTaskMessage, nil); ok {
		t.Fatalf("Publish to unsubscribed agent should return false")
	}
}

func TestInboxHub_ResubscribeEvictsOld(t *testing.T) {
	h := NewInboxHub()
	s1 := h.Subscribe("alice")

	// 第二次订阅应踢掉第一次
	s2 := h.Subscribe("alice")

	select {
	case <-s1.Done:
		// 期望行为
	case <-time.After(time.Second):
		t.Fatalf("s1 should be evicted")
	}

	// s2 能收消息
	h.Publish("alice", InboxEventPing, nil)
	select {
	case <-s2.Events:
	case <-time.After(time.Second):
		t.Fatalf("s2 should receive event")
	}
	h.Unsubscribe("alice", s2)
}

func TestInboxHub_BufferFullDropsEvent(t *testing.T) {
	h := NewInboxHub()
	h.bufSize = 2
	s := h.Subscribe("alice")
	defer h.Unsubscribe("alice", s)

	// 不消费,填满 buffer
	for i := 0; i < 2; i++ {
		if !h.Publish("alice", InboxEventPing, i) {
			t.Fatalf("first %d publishes should succeed", i)
		}
	}
	if h.Publish("alice", InboxEventPing, "overflow") {
		t.Fatalf("3rd publish should be dropped")
	}
}

func TestInboxHub_IsConnected(t *testing.T) {
	h := NewInboxHub()
	if h.IsConnected("x") {
		t.Fatalf("should not be connected")
	}
	s := h.Subscribe("x")
	defer h.Unsubscribe("x", s)
	if !h.IsConnected("x") {
		t.Fatalf("should be connected")
	}
}

func TestInboxHub_PingLoop(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h := NewInboxHub()
	h.StartPingLoop(ctx, 50*time.Millisecond)
	s := h.Subscribe("alice")
	defer h.Unsubscribe("alice", s)

	select {
	case evt := <-s.Events:
		if evt.Kind != InboxEventPing {
			t.Fatalf("expected ping, got %s", evt.Kind)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("no ping received")
	}
}

func TestEncodeSSE(t *testing.T) {
	e := &InboxEvent{Kind: InboxEventTaskMessage, Data: map[string]any{"seq": 1}, Seq: 100}
	out := string(EncodeSSE(e))
	if want := "event: task_message\n"; !contains(out, want) {
		t.Fatalf("missing event line: %q", out)
	}
	if !contains(out, "data: ") || !contains(out, "\n\n") {
		t.Fatalf("malformed SSE: %q", out)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	}()
}
