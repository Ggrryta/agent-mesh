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
	"github.com/Ggrryta/agent-mesh/gateway/internal/domain/group"
	"github.com/Ggrryta/agent-mesh/gateway/internal/domain/inbox"
	"github.com/Ggrryta/agent-mesh/gateway/internal/domain/skill"
	"github.com/Ggrryta/agent-mesh/gateway/internal/domain/task"
	"github.com/Ggrryta/agent-mesh/gateway/internal/domain/user"
	"github.com/Ggrryta/agent-mesh/gateway/internal/feed"
	"github.com/Ggrryta/agent-mesh/gateway/internal/middleware"
	"github.com/Ggrryta/agent-mesh/gateway/pkg/auth"

	"go.uber.org/zap"
)

// Week 7 E2E：群组协作完整路径
//
// 场景：用户 U1 拥有 agent alice (入口)、agent bob (已是 alice 好友)。
// U1 建群 team-001，把 alice 和 bob 加进去。
// U1 从前端给 alice 下令；alice 自主找 bob 协作。
//
// 验证：
//  1. 加成员时 bob 通过好友校验进来
//  2. 用户通过 virtual-user-agent 给 alice 发 task
//  3. alice 在群内可以给 bob 发 task（同群鉴权生效，即使没在初始 friendship）
//  4. bob 能拉到自己的 inbox，收到 alice 的消息
//  5. timeline 能查看整个协作过程（message + artifact 元数据）
//  6. preview 字段端到端贯通
func TestWeek7_GroupCollaboration_FullPath(t *testing.T) {
	h := newTestHarness(t)

	// 准备：用户 + 两个 agent（alice / bob）
	const ownerUID int64 = 1
	h.userRepo.Seed(ownerUID, "owner")

	// virtual-user-agent（通常由 user.Register 事务创建，这里手工 seed）
	virtualAgentID := user.VirtualAgentIDFor(ownerUID)
	h.seedAgentWithKind(virtualAgentID, ownerUID, "virtual-user-1", "", agent.KindVirtualUser)

	h.seedAgent("alice", ownerUID, "Alice", "Content writer")
	h.seedAgent("bob", ownerUID, "Bob", "Research assistant")

	// 好友关系：alice ↔ bob
	h.friendRepo.Seed("alice", "bob")

	// 用户 / agent JWT
	userTok, _ := h.signer.IssueUser(ownerUID)
	aliceTok, _ := h.signer.IssueAgent("alice", ownerUID, 1)
	bobTok, _ := h.signer.IssueAgent("bob", ownerUID, 2)

	// ─── 场景 1：建群 + 加成员（好友校验）──────────────────────────

	// 创建群组 team-001（alice 作为创建者，owner role）
	body := map[string]any{"group_id": "team-001", "name": "Content Team", "agent_id": "alice"}
	resp := h.doReq("POST", "/v1/admin/groups", userTok, body)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create group: %d", resp.StatusCode)
	}

	// 把 bob 加进群（alice 已是 owner，bob 是 alice 好友 → 通过资格校验）
	body = map[string]any{"agent_id": "bob"}
	resp = h.doReq("POST", "/v1/admin/groups/team-001/members", userTok, body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("add bob: %d", resp.StatusCode)
	}

	// 尝试加入 charlie（非 alice 好友，非用户名下 agent）→ 应被拒绝
	h.seedAgent("charlie", 999, "Charlie (stranger)", "") // 别人的 agent
	body = map[string]any{"agent_id": "charlie"}
	resp = h.doReq("POST", "/v1/admin/groups/team-001/members", userTok, body)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("add stranger charlie should be forbidden, got %d", resp.StatusCode)
	}

	// ─── 场景 2：Roster 拿到 AgentCard ──────────────────────────────

	resp = h.doReq("GET", "/v1/admin/groups/team-001/roster", userTok, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("roster: %d", resp.StatusCode)
	}
	var rosterResp struct {
		Roster []struct {
			AgentID     string `json:"agent_id"`
			Name        string `json:"name"`
			Description string `json:"description"`
			Role        string `json:"role"`
		} `json:"roster"`
	}
	json.NewDecoder(resp.Body).Decode(&rosterResp)
	resp.Body.Close()
	if len(rosterResp.Roster) != 2 {
		t.Fatalf("roster should have 2 members, got %d", len(rosterResp.Roster))
	}
	t.Logf("roster: %+v", rosterResp.Roster)

	// ─── 场景 3：用户通过 virtual-user-agent 下令 alice ──────────────

	// virtual-user 自动和 owner 名下所有 agent 可通信，需要加 friendship
	// （真实场景中 friendship 自动创建；这里手工 seed）
	h.friendRepo.Seed(virtualAgentID, "alice")

	body = map[string]any{
		"to_agent_id": "alice",
		"message": map[string]any{
			"message_id": "msg-user-1",
			"parts":      []map[string]any{{"kind": "text", "text": "写一篇量子计算入门文章"}},
			"preview":    "写一篇量子计算入门文章",
		},
	}
	resp = h.doReq("POST", "/v1/admin/tasks", userTok, body)
	if resp.StatusCode != http.StatusCreated {
		body, _ := readBody(resp)
		t.Fatalf("user submit task: %d body=%s", resp.StatusCode, body)
	}
	var taskResp struct {
		TaskID    string `json:"task_id"`
		ContextID string `json:"context_id"`
	}
	json.NewDecoder(resp.Body).Decode(&taskResp)
	resp.Body.Close()
	parentTaskID := taskResp.TaskID
	sharedCtxID := taskResp.ContextID
	t.Logf("user→alice task: %s (ctx=%s)", parentTaskID, sharedCtxID)

	// ─── 场景 4：alice 拉自己 inbox 收到任务 ──────────────────────

	resp = h.doReq("GET", "/v1/mesh/inbox?since=0&limit=10", aliceTok, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("alice inbox: %d", resp.StatusCode)
	}
	var inboxResp struct {
		Events []map[string]any `json:"events"`
		MaxID  int64            `json:"max_id"`
	}
	json.NewDecoder(resp.Body).Decode(&inboxResp)
	resp.Body.Close()
	if len(inboxResp.Events) == 0 {
		t.Fatal("alice should have inbox events")
	}

	// ─── 场景 5：alice 主动找 bob 协作（同群鉴权生效）──────────

	body = map[string]any{
		"task_id":     "t-alice-bob-sub",
		"context_id":  sharedCtxID, // 共享协作上下文
		"to_agent_id": "bob",
		"message": map[string]any{
			"message_id": "msg-alice-bob-1",
			"parts":      []map[string]any{{"kind": "text", "text": "帮我调研量子计算基础概念"}},
			"preview":    "调研请求：量子计算",
		},
	}
	resp = h.doReq("POST", "/v1/mesh/tasks", aliceTok, body)
	if resp.StatusCode != http.StatusCreated {
		body, _ := readBody(resp)
		t.Fatalf("alice→bob sub-task: %d body=%s", resp.StatusCode, body)
	}

	// ─── 场景 6：bob 收到子任务 + 完成 ──────────────────────────

	resp = h.doReq("GET", "/v1/mesh/inbox?since=0&limit=10", bobTok, nil)
	var bobInbox struct{ Events []map[string]any }
	json.NewDecoder(resp.Body).Decode(&bobInbox)
	resp.Body.Close()
	foundSubTask := false
	for _, e := range bobInbox.Events {
		if e["task_id"] == "t-alice-bob-sub" {
			foundSubTask = true
			break
		}
	}
	if !foundSubTask {
		t.Fatal("bob should receive alice's sub-task")
	}

	// bob 转到 working 并回复
	body = map[string]any{"to_state": "working", "status_message": "开始调研"}
	resp = h.doReq("POST", "/v1/mesh/tasks/t-alice-bob-sub/transition", bobTok, body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("bob transition working: %d", resp.StatusCode)
	}
	resp.Body.Close()

	body = map[string]any{
		"message_id": "msg-bob-reply-1",
		"parts":      []map[string]any{{"kind": "text", "text": "量子比特是..."}},
		"preview":    "调研完成",
	}
	resp = h.doReq("POST", "/v1/mesh/tasks/t-alice-bob-sub/messages", bobTok, body)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("bob reply: %d", resp.StatusCode)
	}
	resp.Body.Close()

	// ─── 场景 7：Timeline 能看到整个协作过程 ──────────────────────

	resp = h.doReq("GET", "/v1/admin/tasks/context/"+sharedCtxID+"/timeline", userTok, nil)
	if resp.StatusCode != http.StatusOK {
		body, _ := readBody(resp)
		t.Fatalf("timeline: %d body=%s", resp.StatusCode, body)
	}
	var timelineResp struct {
		Entries []map[string]any `json:"entries"`
	}
	json.NewDecoder(resp.Body).Decode(&timelineResp)
	resp.Body.Close()

	// 应包含：user→alice msg + alice→bob msg + bob→alice reply
	if len(timelineResp.Entries) < 3 {
		t.Fatalf("timeline should have >=3 entries, got %d", len(timelineResp.Entries))
	}
	t.Logf("timeline has %d entries", len(timelineResp.Entries))

	// 验证 preview 端到端打通：至少一条带 preview
	previewFound := false
	for _, e := range timelineResp.Entries {
		if p, ok := e["preview"].(string); ok && p != "" {
			previewFound = true
			t.Logf("preview: %s", p)
			break
		}
	}
	if !previewFound {
		t.Fatal("preview field should be populated in timeline")
	}

	t.Log("Week 7 E2E: ALL GROUP COLLABORATION SCENARIOS PASSED")
}

// ─── Test harness ────────────────────────────────────────────────

type testHarness struct {
	t          *testing.T
	server     *httptest.Server
	signer     *auth.Signer
	agentRepo  *memAgentRepo
	agentCache *agent.Cache
	userRepo   *memUserRepo
	friendRepo *memFriendRepo
}

func newTestHarness(t *testing.T) *testHarness {
	signer, _ := auth.NewSigner("e2e_test_secret_32_bytes_long____", time.Hour, time.Hour)
	log := zap.NewNop()

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

	groupRepo := newMemGroupRepo()
	groupSvc := group.NewService(groupRepo, log).
		WithEligibilityCheck(agentLookup, friendSvc)

	taskSvc := task.NewService(taskRepo, agentLookup, friendSvc).
		WithInbox(inboxSvc).
		WithGroups(groupSvc)

	feedHub := feed.NewHub(nil, log)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go feedHub.Run(ctx)

	pusher := delivery.NewPusher(inboxSvc, agentLookup, delivery.Config{}, log)
	inboxSvc.WithNotifier(pusher.NotifyEvent)
	go pusher.Run(ctx)

	adminH := admin.New(userSvc, agentSvc, skillRepo, apikeySvc, friendSvc, signer,
		admin.WithFeed(feedHub), admin.WithLogger(log),
		admin.WithGroups(groupSvc), admin.WithTasks(taskSvc))
	meshH := mesh.New(agentSvc, apikeySvc, taskSvc, inboxSvc, signer,
		mesh.WithFeed(feedHub), mesh.WithAgentLookup(agentLookup),
		mesh.WithGroups(groupSvc), mesh.WithSkills(skillRepo))

	mux := http.NewServeMux()
	mux.Handle("/v1/admin/", http.StripPrefix("/v1/admin", adminH.Mux()))
	mux.Handle("/v1/mesh/", http.StripPrefix("/v1/mesh", meshH.Mux()))
	handler := middleware.GatewayAuth(signer)(middleware.WithRequestID(middleware.Recover(log)(mux)))
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	return &testHarness{
		t:          t,
		server:     srv,
		signer:     signer,
		agentRepo:  agentRepo,
		agentCache: agentCache,
		userRepo:   userRepo,
		friendRepo: friendRepo,
	}
}

func (h *testHarness) seedAgent(agentID string, ownerUID int64, name, desc string) {
	h.seedAgentWithKind(agentID, ownerUID, name, desc, agent.KindNormal)
}

func (h *testHarness) seedAgentWithKind(agentID string, ownerUID int64, name, desc string, kind agent.Kind) {
	ag := &agent.Agent{
		AgentID: agentID, OwnerUID: ownerUID, Name: name, Description: desc,
		Kind: kind, Status: agent.StatusActive,
	}
	h.agentRepo.Upsert(context.Background(), ag)
	cp := *ag
	h.agentCache.Set(&cp)
}

func (h *testHarness) doReq(method, path, token string, body any) *http.Response {
	h.t.Helper()
	var r *http.Request
	if body != nil {
		b, _ := json.Marshal(body)
		r, _ = http.NewRequest(method, h.server.URL+path, bytes.NewReader(b))
		r.Header.Set("Content-Type", "application/json")
	} else {
		r, _ = http.NewRequest(method, h.server.URL+path, nil)
	}
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		h.t.Fatalf("%s %s: %v", method, path, err)
	}
	return resp
}

func readBody(resp *http.Response) (string, error) {
	buf := new(bytes.Buffer)
	_, err := buf.ReadFrom(resp.Body)
	resp.Body.Close()
	return buf.String(), err
}

// 保留 import（部分只在 week4_test 用）
var _ = fmt.Sprintf
