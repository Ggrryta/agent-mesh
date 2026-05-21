package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Ggrryta/agent-mesh/gateway/internal/api/admin"
	"github.com/Ggrryta/agent-mesh/gateway/internal/api/mesh"
	"github.com/Ggrryta/agent-mesh/gateway/internal/delivery"
	"github.com/Ggrryta/agent-mesh/gateway/internal/domain/agent"
	"github.com/Ggrryta/agent-mesh/gateway/internal/domain/apikey"
	"github.com/Ggrryta/agent-mesh/gateway/internal/domain/friendship"
	"github.com/Ggrryta/agent-mesh/gateway/internal/domain/inbox"
	"github.com/Ggrryta/agent-mesh/gateway/internal/domain/skill"
	"github.com/Ggrryta/agent-mesh/gateway/internal/domain/task"
	"github.com/Ggrryta/agent-mesh/gateway/internal/domain/user"
	"github.com/Ggrryta/agent-mesh/gateway/internal/feed"
	"github.com/Ggrryta/agent-mesh/gateway/internal/middleware"
	"github.com/Ggrryta/agent-mesh/gateway/pkg/auth"

	"github.com/gorilla/websocket"
	"go.uber.org/zap"
)

// PLACEHOLDER_E2E_CONTINUE

// Week 4 E2E：验证 Alice→Bob 点对点 + 前端 WebSocket 实时收到事件。
// 全内存（memRepo），不依赖 MySQL/Redis/k3d。
func TestWeek4_AliceBob_PointToPoint_WithFeed(t *testing.T) {
	signer, _ := auth.NewSigner("e2e_test_secret_32_bytes_long____", time.Hour, time.Hour)
	log := zap.NewNop()

	// 构建 domain 层
	userRepo := newMemUserRepo()
	userSvc := user.NewService(userRepo)
	agentRepo := newMemAgentRepo()
	agentCache := agent.NewCache()
	skillRepo := &noopSkillRepo{}
	agentSvc := agent.NewService(agentRepo, agentCache).WithSkills(skill.NewAdapter(skillRepo))
	apikeyRepo := newMemAPIKeyRepo()
	apikeySvc := apikey.NewService(apikeyRepo, nil)
	friendRepo := newMemFriendRepo()
	agentLookup := agent.NewLookupAdapter(agentSvc)
	friendSvc := friendship.NewService(friendRepo, agentLookup)
	taskRepo := newMemTaskRepo()
	inboxRepo := newMemInboxRepo()
	inboxSvc := inbox.NewService(inboxRepo)
	taskSvc := task.NewService(taskRepo, agentLookup, friendSvc).WithInbox(inboxSvc)

	// FeedHub（本地模式，无 Redis）
	feedHub := feed.NewHub(nil, log)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go feedHub.Run(ctx)

	// Push worker
	pusher := delivery.NewPusher(inboxSvc, agentLookup, delivery.Config{}, log)
	inboxSvc.WithNotifier(pusher.NotifyEvent)
	go pusher.Run(ctx)

	// 构建 HTTP handler
	adminH := admin.New(userSvc, agentSvc, skillRepo, apikeySvc, friendSvc, signer,
		admin.WithFeed(feedHub), admin.WithLogger(log))
	meshH := mesh.New(agentSvc, apikeySvc, taskSvc, inboxSvc, signer,
		mesh.WithFeed(feedHub), mesh.WithAgentLookup(agentLookup))

	mux := http.NewServeMux()
	mux.Handle("/v1/admin/", http.StripPrefix("/v1/admin", adminH.Mux()))
	mux.Handle("/v1/mesh/", http.StripPrefix("/v1/mesh", meshH.Mux()))

	handler := middleware.GatewayAuth(signer)(middleware.WithRequestID(middleware.Recover(log)(mux)))
	srv := httptest.NewServer(handler)
	defer srv.Close()

	// ─── 准备数据：用户 + agent + 好友 ───────────────────────────

	// 创建用户（owner）
	ownerUID := int64(1)
	userRepo.Seed(ownerUID, "owner")

	// 创建 alice 和 bob
	agentRepo.Upsert(context.Background(), &agent.Agent{
		AgentID: "alice", OwnerUID: ownerUID, Name: "Alice",
		Kind: agent.KindNormal, Status: agent.StatusActive,
	})
	agentRepo.Upsert(context.Background(), &agent.Agent{
		AgentID: "bob", OwnerUID: ownerUID, Name: "Bob",
		Kind: agent.KindNormal, Status: agent.StatusActive,
	})
	agentCache.Set(&agent.Agent{AgentID: "alice", OwnerUID: ownerUID, Kind: agent.KindNormal, Status: agent.StatusActive})
	agentCache.Set(&agent.Agent{AgentID: "bob", OwnerUID: ownerUID, Kind: agent.KindNormal, Status: agent.StatusActive})

	// 建立好友关系
	friendRepo.Seed("alice", "bob")

	// 签发 agent JWT
	aliceTok, _ := signer.IssueAgent("alice", ownerUID, 1)
	bobTok, _ := signer.IssueAgent("bob", ownerUID, 2)
	userTok, _ := signer.IssueUser(ownerUID)

	// ─── 前端 WebSocket 连接 ─────────────────────────────────────

	wsURL := "ws" + srv.URL[4:] + "/v1/admin/ws/feed"
	wsHeader := http.Header{"Authorization": {"Bearer " + userTok}}
	wsConn, _, err := websocket.DefaultDialer.Dial(wsURL, wsHeader)
	if err != nil {
		t.Fatalf("ws dial: %v", err)
	}
	defer wsConn.Close()

	feedEvents := make(chan feed.FeedEvent, 10)
	go func() {
		for {
			var ev feed.FeedEvent
			if err := wsConn.ReadJSON(&ev); err != nil {
				return
			}
			feedEvents <- ev
		}
	}()

	// ─── 场景 1：Alice 给 Bob 发消息 ────────────────────────────

	submitBody, _ := json.Marshal(map[string]any{
		"task_id":     "t-e2e-alice-bob-001",
		"context_id":  "ctx-e2e-001",
		"to_agent_id": "bob",
		"message": map[string]any{
			"message_id": "msg-alice-1",
			"parts":      []map[string]any{{"kind": "text", "text": "你好 Bob，帮我总结一下"}},
		},
	})
	resp := doReq(t, srv.URL, "POST", "/v1/mesh/tasks", aliceTok, submitBody)
	if resp.StatusCode != http.StatusCreated {
		body := new(bytes.Buffer)
		body.ReadFrom(resp.Body)
		resp.Body.Close()
		t.Fatalf("submit: %d body=%s", resp.StatusCode, body.String())
	}
	var taskResp struct {
		TaskID string `json:"task_id"`
	}
	json.NewDecoder(resp.Body).Decode(&taskResp)
	resp.Body.Close()
	taskID := taskResp.TaskID
	t.Logf("task created: %s", taskID)

	// 验证前端收到 task_created 事件（alice 和 bob 各一个，因为同 owner）
	assertFeedEvent(t, feedEvents, "task_created", 2*time.Second)
	assertFeedEvent(t, feedEvents, "task_created", 2*time.Second)

	// ─── 场景 2：Bob 拉 inbox 收到消息 ──────────────────────────

	resp = doReq(t, srv.URL, "GET", "/v1/mesh/inbox?since=0&limit=10", bobTok, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("bob inbox: %d", resp.StatusCode)
	}
	var inboxResp struct {
		Events []json.RawMessage `json:"events"`
		MaxID  int64             `json:"max_id"`
	}
	json.NewDecoder(resp.Body).Decode(&inboxResp)
	resp.Body.Close()
	if len(inboxResp.Events) == 0 {
		t.Fatal("bob should have inbox events")
	}
	t.Logf("bob inbox: %d events, max_id=%d", len(inboxResp.Events), inboxResp.MaxID)

	// ─── 场景 3：Bob 回复 ────────────────────────────────────────

	replyBody, _ := json.Marshal(map[string]any{
		"message_id": "msg-bob-1",
		"parts":      []map[string]any{{"kind": "text", "text": "总结如下：1. 核心观点..."}},
	})
	resp = doReq(t, srv.URL, "POST", fmt.Sprintf("/v1/mesh/tasks/%s/messages", taskID), bobTok, replyBody)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("bob reply: %d", resp.StatusCode)
	}
	resp.Body.Close()

	// 验证前端收到 task_message 事件
	assertFeedEvent(t, feedEvents, "task_message", 2*time.Second)

	// ─── 场景 4：Bob 标记 working 然后 completed ─────────────────

	workingBody, _ := json.Marshal(map[string]any{
		"to_state":       "working",
		"status_message": "开始处理",
	})
	resp = doReq(t, srv.URL, "POST", fmt.Sprintf("/v1/mesh/tasks/%s/transition", taskID), bobTok, workingBody)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("transition working: %d", resp.StatusCode)
	}
	resp.Body.Close()

	transBody, _ := json.Marshal(map[string]any{
		"to_state":       "completed",
		"status_message": "总结完成",
	})
	resp = doReq(t, srv.URL, "POST", fmt.Sprintf("/v1/mesh/tasks/%s/transition", taskID), bobTok, transBody)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("transition: %d", resp.StatusCode)
	}
	resp.Body.Close()

	// 验证前端收到 task_transition 事件（working + completed）
	assertFeedEvent(t, feedEvents, "task_transition", 2*time.Second)
	assertFeedEvent(t, feedEvents, "task_transition", 2*time.Second)

	// ─── 场景 5：Alice long-poll 收到回复 ────────────────────────

	resp = doReq(t, srv.URL, "GET", "/v1/mesh/inbox?since=0&limit=10", aliceTok, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("alice inbox: %d", resp.StatusCode)
	}
	var aliceInbox struct {
		Events []json.RawMessage `json:"events"`
	}
	json.NewDecoder(resp.Body).Decode(&aliceInbox)
	resp.Body.Close()
	if len(aliceInbox.Events) < 2 {
		t.Fatalf("alice should have >=2 events (reply + transition), got %d", len(aliceInbox.Events))
	}
	t.Logf("alice inbox: %d events", len(aliceInbox.Events))

	// ─── 场景 6：Long-poll wait 验证 ────────────────────────────

	start := time.Now()
	resp = doReq(t, srv.URL, "GET", fmt.Sprintf("/v1/mesh/inbox?since=%d&wait=1", inboxResp.MaxID+1000), bobTok, nil)
	elapsed := time.Since(start)
	resp.Body.Close()
	if elapsed < 900*time.Millisecond {
		t.Fatalf("long-poll should wait ~1s, returned in %v", elapsed)
	}
	t.Logf("long-poll wait verified: %v", elapsed)

	t.Log("Week 4 E2E: ALL SCENARIOS PASSED")
}

// ─── helpers ─────────────────────────────────────────────────────

func doReq(t *testing.T, base, method, path, token string, body []byte) *http.Response {
	t.Helper()
	var r *http.Request
	if body != nil {
		r, _ = http.NewRequest(method, base+path, bytes.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	} else {
		r, _ = http.NewRequest(method, base+path, nil)
	}
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	return resp
}

func assertFeedEvent(t *testing.T, ch chan feed.FeedEvent, expectedType string, timeout time.Duration) {
	t.Helper()
	select {
	case ev := <-ch:
		if ev.Type != expectedType {
			t.Fatalf("feed event: want type=%s, got %s", expectedType, ev.Type)
		}
	case <-time.After(timeout):
		t.Fatalf("timeout waiting for feed event type=%s", expectedType)
	}
}
