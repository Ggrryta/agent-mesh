package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Ggrryta/agent-mesh/gateway/internal/feed"
	"github.com/Ggrryta/agent-mesh/gateway/internal/middleware"
	"github.com/Ggrryta/agent-mesh/gateway/pkg/auth"

	"github.com/gorilla/websocket"
	"go.uber.org/zap"
)

func TestWSFeed_ReceivesEvents(t *testing.T) {
	signer, _ := auth.NewSigner("test_secret_for_ws_feed_handler___", time.Hour, time.Hour)
	hub := feed.NewHub(nil, zap.NewNop())

	h := New(nil, nil, nil, nil, nil, signer,
		WithFeed(hub), WithLogger(zap.NewNop()))

	mux := http.NewServeMux()
	mux.Handle("GET /ws/feed", middleware.RequireUser(signer, http.HandlerFunc(h.handleWSFeed)))

	srv := httptest.NewServer(mux)
	defer srv.Close()

	tok, _ := signer.IssueUser(42)
	wsURL := "ws" + srv.URL[4:] + "/ws/feed"
	header := http.Header{"Authorization": {"Bearer " + tok}}

	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, header)
	if err != nil {
		t.Fatalf("dial: %v (resp=%v)", err, resp)
	}
	defer conn.Close()

	hub.Publish(context.Background(), 42, &feed.FeedEvent{
		Type:    "task_message",
		AgentID: "alice",
		TaskID:  "t1",
		Payload: json.RawMessage(`{"text":"hello"}`),
	})

	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	var got feed.FeedEvent
	if err := conn.ReadJSON(&got); err != nil {
		t.Fatalf("read: %v", err)
	}
	if got.Type != "task_message" || got.AgentID != "alice" || got.TaskID != "t1" {
		t.Fatalf("unexpected event: %+v", got)
	}
}

func TestWSFeed_NoAuth_Rejected(t *testing.T) {
	signer, _ := auth.NewSigner("test_secret_for_ws_feed_handler___", time.Hour, time.Hour)
	hub := feed.NewHub(nil, zap.NewNop())

	h := New(nil, nil, nil, nil, nil, signer,
		WithFeed(hub), WithLogger(zap.NewNop()))

	mux := http.NewServeMux()
	mux.Handle("GET /ws/feed", middleware.RequireUser(signer, http.HandlerFunc(h.handleWSFeed)))

	srv := httptest.NewServer(mux)
	defer srv.Close()

	wsURL := "ws" + srv.URL[4:] + "/ws/feed"
	_, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err == nil {
		t.Fatal("should fail without auth")
	}
	if resp != nil && resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", resp.StatusCode)
	}
}

func TestWSFeed_OtherUser_NoLeak(t *testing.T) {
	signer, _ := auth.NewSigner("test_secret_for_ws_feed_handler___", time.Hour, time.Hour)
	hub := feed.NewHub(nil, zap.NewNop())

	h := New(nil, nil, nil, nil, nil, signer,
		WithFeed(hub), WithLogger(zap.NewNop()))

	mux := http.NewServeMux()
	mux.Handle("GET /ws/feed", middleware.RequireUser(signer, http.HandlerFunc(h.handleWSFeed)))

	srv := httptest.NewServer(mux)
	defer srv.Close()

	tok, _ := signer.IssueUser(99)
	wsURL := "ws" + srv.URL[4:] + "/ws/feed"
	header := http.Header{"Authorization": {"Bearer " + tok}}

	conn, _, err := websocket.DefaultDialer.Dial(wsURL, header)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	// 推给 uid=42 的事件，uid=99 不应收到
	hub.Publish(context.Background(), 42, &feed.FeedEvent{Type: "task_message", AgentID: "alice", TaskID: "t1"})

	conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	var got feed.FeedEvent
	err = conn.ReadJSON(&got)
	if err == nil {
		t.Fatalf("uid=99 should not receive uid=42's event, got: %+v", got)
	}
}
