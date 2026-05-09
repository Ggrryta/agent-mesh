package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"testing"
	"time"

	"agent-gateway/internal/model"
	"agent-gateway/internal/repo"
	"agent-gateway/internal/service"
	"agent-gateway/pkg/circuitbreaker"
	"agent-gateway/pkg/ctxkey"

	"github.com/cloudwego/hertz/pkg/app"
	hconfig "github.com/cloudwego/hertz/pkg/common/config"
	"github.com/cloudwego/hertz/pkg/common/ut"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
	"github.com/cloudwego/hertz/pkg/route"
)

type stubTaskRepo struct {
	tasks map[string]*model.AsyncTask
}

func (r *stubTaskRepo) Create(_ context.Context, t *model.AsyncTask) error {
	if r.tasks == nil {
		r.tasks = make(map[string]*model.AsyncTask)
	}
	r.tasks[t.TaskID] = t
	return nil
}

func (r *stubTaskRepo) GetByTaskID(_ context.Context, taskID string) (*model.AsyncTask, error) {
	task, ok := r.tasks[taskID]
	if !ok {
		return nil, repo.ErrTaskNotFound
	}
	return task, nil
}

type stubConsumerRepo struct {
	created bool
}

type stubAgentSkillRepo struct {
	skills map[string]*model.AgentSkill
}

type stubAgentPermissionRepo struct {
	allowed map[string]bool
}

type stubAgentService struct {
	agents map[string]*model.Agent
	skills map[string][]*model.AgentSkill
}

type stubA2AInvoker struct {
	lastSkillID string
	lastInput   json.RawMessage
}

func (r *stubAgentSkillRepo) GetByAgentIDAndSkillID(_ context.Context, agentID, skillID string) (*model.AgentSkill, error) {
	skill, ok := r.skills[agentID+"/"+skillID]
	if !ok {
		return nil, repo.ErrAgentSkillNotFound
	}
	return skill, nil
}

func (r *stubAgentSkillRepo) ListByAgentID(_ context.Context, agentID string) ([]*model.AgentSkill, error) {
	var out []*model.AgentSkill
	for _, skill := range r.skills {
		if skill.AgentID == agentID {
			out = append(out, skill)
		}
	}
	return out, nil
}

func (i *stubA2AInvoker) Send(_ context.Context, _ string, input json.RawMessage, skillID string) (json.RawMessage, error) {
	i.lastSkillID = skillID
	i.lastInput = append(json.RawMessage(nil), input...)
	return json.RawMessage(`{"ok":true}`), nil
}

func (i *stubA2AInvoker) Stream(_ context.Context, _ string, _ json.RawMessage, skillID string, w io.Writer) error {
	i.lastSkillID = skillID
	_, err := w.Write([]byte("data: ok\n\n"))
	return err
}

func (r *stubAgentPermissionRepo) HasPermission(_ context.Context, agentID, consumerAppID string) (bool, error) {
	return r.allowed[agentID+":"+consumerAppID], nil
}

func (s *stubAgentService) Register(_ context.Context, _ string, _ service.RegisterAgentRequest) (*model.Agent, error) {
	return nil, nil
}

func (s *stubAgentService) Deregister(_ context.Context, _ string, _ string) error {
	return nil
}

func (s *stubAgentService) Get(_ context.Context, agentID string) (*model.Agent, error) {
	agent, ok := s.agents[agentID]
	if !ok {
		return nil, repo.ErrAgentNotFound
	}
	return agent, nil
}

func (s *stubAgentService) Drain(_ context.Context, _ string, _ string) error {
	return nil
}

func (s *stubAgentService) List(_ context.Context, f repo.AgentFilter) ([]*model.Agent, int64, error) {
	var out []*model.Agent
	for _, agent := range s.agents {
		if agent.Status != model.AgentStatusActive {
			continue
		}
		if f.OwnerAppID != "" && agent.OwnerAppID != f.OwnerAppID {
			continue
		}
		out = append(out, agent)
	}
	return out, int64(len(out)), nil
}

func (s *stubAgentService) GetSkills(_ context.Context, agentID string) ([]*model.AgentSkill, error) {
	return s.skills[agentID], nil
}

func (r *stubConsumerRepo) Create(_ context.Context, _ *model.Consumer) error {
	r.created = true
	return nil
}

func newTestEngine() *route.Engine {
	return route.NewEngine(hconfig.NewOptions(nil))
}

func TestGatewayHandler_GetTask_RejectsCrossTenantAccess(t *testing.T) {
	repoStub := &stubTaskRepo{
		tasks: map[string]*model.AsyncTask{
			"task-1": {
				TaskID:    "task-1",
				AgentID:   "agent-1",
				AppID:     "owner-app",
				Status:    model.AsyncTaskStatusCompleted,
				CreatedAt: time.Now(),
				UpdatedAt: time.Now(),
			},
		},
	}
	h := &GatewayHandler{taskRepo: repoStub}

	engine := newTestEngine()
	engine.GET("/gateway/task/:task_id", func(ctx context.Context, c *app.RequestContext) {
		c.Set(ctxkey.AppID, "other-app")
		h.GetTask(ctx, c)
	})

	resp := ut.PerformRequest(engine, consts.MethodGet, "/gateway/task/task-1", nil).Result()
	if resp.StatusCode() != consts.StatusForbidden {
		t.Fatalf("expected 403, got %d", resp.StatusCode())
	}
}

func TestGatewayHandler_GetTask_AllowsOwner(t *testing.T) {
	repoStub := &stubTaskRepo{
		tasks: map[string]*model.AsyncTask{
			"task-1": {
				TaskID:    "task-1",
				AgentID:   "agent-1",
				AppID:     "owner-app",
				Status:    model.AsyncTaskStatusCompleted,
				Output:    json.RawMessage(`{"ok":true}`),
				CreatedAt: time.Now(),
				UpdatedAt: time.Now(),
			},
		},
	}
	h := &GatewayHandler{taskRepo: repoStub}

	engine := newTestEngine()
	engine.GET("/gateway/task/:task_id", func(ctx context.Context, c *app.RequestContext) {
		c.Set(ctxkey.AppID, "owner-app")
		h.GetTask(ctx, c)
	})

	resp := ut.PerformRequest(engine, consts.MethodGet, "/gateway/task/task-1", nil).Result()
	if resp.StatusCode() != consts.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode())
	}
	if !bytes.Contains(resp.Body(), []byte(`"task_id":"task-1"`)) {
		t.Fatalf("expected task payload, got %s", resp.Body())
	}
}

func TestConsumerHandler_Register_RejectsWeakSecret(t *testing.T) {
	repoStub := &stubConsumerRepo{}
	h := &ConsumerHandler{repo: repoStub}

	engine := newTestEngine()
	engine.POST("/register", h.Register)

	body := ut.Body{
		Body: bytes.NewBufferString(`{"app_id":"new-app","secret":"short"}`),
		Len:  len(`{"app_id":"new-app","secret":"short"}`),
	}
	resp := ut.PerformRequest(
		engine,
		consts.MethodPost,
		"/register",
		&body,
		ut.Header{Key: "Content-Type", Value: "application/json"},
	).Result()
	if resp.StatusCode() != consts.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode())
	}
	if repoStub.created {
		t.Fatalf("expected repo not called")
	}
}

func TestConsumerHandler_Register_RejectsInvalidAppID(t *testing.T) {
	repoStub := &stubConsumerRepo{}
	h := &ConsumerHandler{repo: repoStub}

	engine := newTestEngine()
	engine.POST("/register", h.Register)

	body := ut.Body{
		Body: bytes.NewBufferString(`{"app_id":"AB","secret":"valid-secret-12345"}`),
		Len:  len(`{"app_id":"AB","secret":"valid-secret-12345"}`),
	}
	resp := ut.PerformRequest(
		engine,
		consts.MethodPost,
		"/register",
		&body,
		ut.Header{Key: "Content-Type", Value: "application/json"},
	).Result()
	if resp.StatusCode() != consts.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode())
	}
	if repoStub.created {
		t.Fatalf("expected repo not called")
	}
}

func TestConsumerHandler_Register_Success(t *testing.T) {
	repoStub := &stubConsumerRepo{}
	h := &ConsumerHandler{repo: repoStub}

	engine := newTestEngine()
	engine.POST("/register", h.Register)

	body := ut.Body{
		Body: bytes.NewBufferString(`{"app_id":"new-app","secret":"valid-secret-12345"}`),
		Len:  len(`{"app_id":"new-app","secret":"valid-secret-12345"}`),
	}
	resp := ut.PerformRequest(
		engine,
		consts.MethodPost,
		"/register",
		&body,
		ut.Header{Key: "Content-Type", Value: "application/json"},
	).Result()
	if resp.StatusCode() != consts.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", resp.StatusCode(), resp.Body())
	}
	if !repoStub.created {
		t.Fatalf("expected repo called")
	}
}

func TestAgentHandler_Get_HidesPrivateAgentFromAnonymous(t *testing.T) {
	h := &AgentHandler{
		svc: &stubAgentService{
			agents: map[string]*model.Agent{
				"private-agent": {
					AgentID:    "private-agent",
					OwnerAppID: "owner-app",
					Status:     model.AgentStatusActive,
					Visibility: model.VisibilityPrivate,
				},
			},
		},
	}

	engine := newTestEngine()
	engine.GET("/agents/:agent_id", func(ctx context.Context, c *app.RequestContext) {
		h.Get(ctx, c)
	})

	resp := ut.PerformRequest(engine, consts.MethodGet, "/agents/private-agent", nil).Result()
	if resp.StatusCode() != consts.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode())
	}
}

func TestAgentHandler_Get_AllowsPermittedPrivateAgent(t *testing.T) {
	h := &AgentHandler{
		svc: &stubAgentService{
			agents: map[string]*model.Agent{
				"private-agent": {
					AgentID:    "private-agent",
					OwnerAppID: "owner-app",
					Status:     model.AgentStatusActive,
					Visibility: model.VisibilityPrivate,
				},
			},
		},
		permRepo: &stubAgentPermissionRepo{
			allowed: map[string]bool{"private-agent:caller-app": true},
		},
	}

	engine := newTestEngine()
	engine.GET("/agents/:agent_id", func(ctx context.Context, c *app.RequestContext) {
		c.Set(ctxkey.AppID, "caller-app")
		h.Get(ctx, c)
	})

	resp := ut.PerformRequest(engine, consts.MethodGet, "/agents/private-agent", nil).Result()
	if resp.StatusCode() != consts.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", resp.StatusCode(), resp.Body())
	}
}

func TestAgentHandler_List_HidesPrivateAgentsFromAnonymous(t *testing.T) {
	h := &AgentHandler{
		svc: &stubAgentService{
			agents: map[string]*model.Agent{
				"public-agent": {
					AgentID:    "public-agent",
					OwnerAppID: "owner-app",
					Status:     model.AgentStatusActive,
					Visibility: model.VisibilityPublic,
				},
				"private-agent": {
					AgentID:    "private-agent",
					OwnerAppID: "owner-app",
					Status:     model.AgentStatusActive,
					Visibility: model.VisibilityPrivate,
				},
			},
		},
	}

	engine := newTestEngine()
	engine.GET("/agents", func(ctx context.Context, c *app.RequestContext) {
		h.List(ctx, c)
	})

	resp := ut.PerformRequest(engine, consts.MethodGet, "/agents", nil).Result()
	if resp.StatusCode() != consts.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", resp.StatusCode(), resp.Body())
	}
	if !bytes.Contains(resp.Body(), []byte(`"public-agent"`)) {
		t.Fatalf("expected public agent in response, got %s", resp.Body())
	}
	if bytes.Contains(resp.Body(), []byte(`"private-agent"`)) {
		t.Fatalf("expected private agent to be hidden, got %s", resp.Body())
	}
	if !bytes.Contains(resp.Body(), []byte(`"total":1`)) {
		t.Fatalf("expected filtered total, got %s", resp.Body())
	}
}

func TestGatewayHandler_InvokeAgentSkill_RejectsUnknownSkill(t *testing.T) {
	cache := service.NewAgentCache(nil)
	cache.Set(&model.Agent{
		AgentID: "agent-1",
		URL:     "http://example.com",
		Status:  model.AgentStatusActive,
	})
	h := &GatewayHandler{
		agentCache:     cache,
		agentSkillRepo: &stubAgentSkillRepo{skills: map[string]*model.AgentSkill{}},
		taskRepo:       &stubTaskRepo{},
	}

	engine := newTestEngine()
	engine.POST("/gateway/invoke/agent/:agent_id/skill/:skill_id", func(ctx context.Context, c *app.RequestContext) {
		h.InvokeAgent(ctx, c)
	})

	body := ut.Body{
		Body: bytes.NewBufferString(`{"text":"hello"}`),
		Len:  len(`{"text":"hello"}`),
	}
	resp := ut.PerformRequest(
		engine,
		consts.MethodPost,
		"/gateway/invoke/agent/agent-1/skill/missing",
		&body,
		ut.Header{Key: "Content-Type", Value: "application/json"},
	).Result()
	if resp.StatusCode() != consts.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode())
	}
}

func TestGatewayHandler_InvokeAgentSkill_ForwardsSkillID(t *testing.T) {
	cache := service.NewAgentCache(nil)
	cache.Set(&model.Agent{
		AgentID: "agent-1",
		URL:     "http://example.com",
		Status:  model.AgentStatusActive,
	})
	invoker := &stubA2AInvoker{}
	h := &GatewayHandler{
		agentCache:     cache,
		agentSkillRepo: &stubAgentSkillRepo{skills: map[string]*model.AgentSkill{"agent-1/translate": {AgentID: "agent-1", SkillID: "translate"}}},
		a2aInvoker:     invoker,
		callGuard:      service.NewAgentCallGuard(circuitbreaker.NewBreakerFactory(nil)),
		taskRepo:       &stubTaskRepo{},
	}

	engine := newTestEngine()
	engine.POST("/gateway/invoke/agent/:agent_id/skill/:skill_id", func(ctx context.Context, c *app.RequestContext) {
		h.InvokeAgent(ctx, c)
	})

	body := ut.Body{
		Body: bytes.NewBufferString(`{"text":"hello"}`),
		Len:  len(`{"text":"hello"}`),
	}
	resp := ut.PerformRequest(
		engine,
		consts.MethodPost,
		"/gateway/invoke/agent/agent-1/skill/translate",
		&body,
		ut.Header{Key: "Content-Type", Value: "application/json"},
	).Result()
	if resp.StatusCode() != consts.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", resp.StatusCode(), resp.Body())
	}
	if invoker.lastSkillID != "translate" {
		t.Fatalf("expected forwarded skill_id, got %q", invoker.lastSkillID)
	}
}

func TestMCPHandler_ToolsCall_RejectsUnknownSkill(t *testing.T) {
	cache := service.NewAgentCache(nil)
	cache.Set(&model.Agent{
		AgentID: "agent-1",
		URL:     "http://example.com",
		Status:  model.AgentStatusActive,
	})
	h := &MCPHandler{
		agentCache:     cache,
		agentSkillRepo: &stubAgentSkillRepo{skills: map[string]*model.AgentSkill{}},
	}

	params := json.RawMessage(`{"name":"agent-1/missing","arguments":{"text":"hello"}}`)
	_, rpcErr := h.handleToolsCall(context.Background(), "caller-app", params)
	if rpcErr == nil || rpcErr.Code != -32602 {
		t.Fatalf("expected -32602 for missing skill, got %#v", rpcErr)
	}
}
