package task

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// ─── memRepo：service 测试用 ─────────────────────────────────────────

type memRepo struct {
	mu        sync.Mutex
	tasks     map[string]*Task
	messages  map[string]*Message // by message_id
	artifacts map[string]*Artifact
	nextID    int64
}

func newMemRepo() *memRepo {
	return &memRepo{
		tasks:     map[string]*Task{},
		messages:  map[string]*Message{},
		artifacts: map[string]*Artifact{},
	}
}

func (r *memRepo) CreateTask(_ context.Context, t *Task, first *Message) (*Task, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if existing, ok := r.tasks[t.TaskID]; ok {
		return cloneTask(existing), nil // 幂等
	}
	r.nextID++
	cp := *t
	cp.ID = r.nextID
	cp.CreatedAt = time.Now()
	cp.UpdatedAt = time.Now()
	r.tasks[t.TaskID] = &cp
	r.messages[first.MessageID] = cloneMessage(first)
	return cloneTask(&cp), nil
}

func (r *memRepo) AppendMessage(_ context.Context, m *Message) (*Message, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if existing, ok := r.messages[m.MessageID]; ok {
		if existing.TaskID != m.TaskID || existing.Role != m.Role {
			return nil, ErrMessageIDDuplicate
		}
		return cloneMessage(existing), nil
	}
	r.nextID++
	cp := *m
	cp.ID = r.nextID
	cp.CreatedAt = time.Now()
	r.messages[m.MessageID] = &cp
	return cloneMessage(&cp), nil
}

func (r *memRepo) AppendArtifact(_ context.Context, a *Artifact) (*Artifact, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := a.TaskID + "|" + a.ArtifactID
	if _, dup := r.artifacts[key]; dup {
		return nil, ErrArtifactIDDuplicate
	}
	r.nextID++
	cp := *a
	cp.ID = r.nextID
	cp.CreatedAt = time.Now()
	r.artifacts[key] = &cp
	return cloneArtifact(&cp), nil
}

func (r *memRepo) TransitionStatus(_ context.Context, taskID string, fromStates []State, to State, statusMessage, errorMsg string) (bool, *Task, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	t, ok := r.tasks[taskID]
	if !ok {
		return false, nil, ErrTaskNotFound
	}
	matched := false
	for _, f := range fromStates {
		if t.Status == f {
			matched = true
			break
		}
	}
	if !matched {
		return false, cloneTask(t), nil
	}
	t.Status = to
	t.StatusMessage = statusMessage
	t.ErrorMsg = errorMsg
	t.UpdatedAt = time.Now()
	return true, cloneTask(t), nil
}

func (r *memRepo) GetTask(_ context.Context, taskID string, withHistory, withArtifacts bool) (*Task, []*Message, []*Artifact, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	t, ok := r.tasks[taskID]
	if !ok {
		return nil, nil, nil, ErrTaskNotFound
	}
	var history []*Message
	var arts []*Artifact
	if withHistory {
		for _, m := range r.messages {
			if m.TaskID == taskID {
				history = append(history, cloneMessage(m))
			}
		}
	}
	if withArtifacts {
		for _, a := range r.artifacts {
			if a.TaskID == taskID {
				arts = append(arts, cloneArtifact(a))
			}
		}
	}
	return cloneTask(t), history, arts, nil
}

func (r *memRepo) ListByContext(_ context.Context, contextID string) ([]*Task, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*Task, 0)
	for _, t := range r.tasks {
		if t.ContextID == contextID {
			out = append(out, cloneTask(t))
		}
	}
	return out, nil
}

func (r *memRepo) GetMessageByID(_ context.Context, messageID string) (*Message, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	m, ok := r.messages[messageID]
	if !ok {
		return nil, ErrMessageNotFound
	}
	return cloneMessage(m), nil
}

func (r *memRepo) ListTimeline(_ context.Context, _ string, _ int64, _ int) ([]*TimelineEntry, error) {
	return nil, nil
}

func (r *memRepo) ListRecentByAgents(_ context.Context, _ []string, _ int) ([]*Task, error) {
	return nil, nil
}

func (r *memRepo) UpdateChatStreak(_ context.Context, _ string, _ bool) error { return nil }

func (r *memRepo) ListChatterTasks(_ context.Context, _ int, _ time.Time) ([]*Task, error) {
	return nil, nil
}

func (r *memRepo) DeleteTerminalTasksBefore(_ context.Context, _ int64, _ time.Time) (int, error) {
	return 0, nil
}

func (r *memRepo) DeleteTaskByID(_ context.Context, taskID string) error {
	delete(r.tasks, taskID)
	return nil
}

func (r *memRepo) TouchActivity(_ context.Context, _ string) error { return nil }

func (r *memRepo) ListInactiveNonTerminal(_ context.Context, _ time.Time) ([]*Task, error) {
	return nil, nil
}

func cloneTask(t *Task) *Task             { cp := *t; return &cp }
func cloneMessage(m *Message) *Message    { cp := *m; return &cp }
func cloneArtifact(a *Artifact) *Artifact { cp := *a; return &cp }

// ─── memAgents / memFriends ────────────────────────────────────────

type memAgents map[string]struct {
	ownerUID int64
	kind     string
}

func (m memAgents) Lookup(_ context.Context, id string) (int64, string, bool) {
	e, ok := m[id]
	if !ok {
		return 0, "", false
	}
	return e.ownerUID, e.kind, true
}

type memFriends struct {
	pairs map[string]bool // "alice|bob" 或 "bob|alice"
	err   error
}

func (m *memFriends) AreFriends(_ context.Context, a, b string) (bool, error) {
	if m.err != nil {
		return false, m.err
	}
	return m.pairs[a+"|"+b] || m.pairs[b+"|"+a], nil
}

func defaultAgents() memAgents {
	return memAgents{
		"alice":          {ownerUID: 1, kind: "normal"},
		"bob":            {ownerUID: 2, kind: "normal"},
		"virtual-user-1": {ownerUID: 1, kind: "virtual-user"},
		"virtual-user-2": {ownerUID: 2, kind: "virtual-user"},
	}
}

func defaultFriends() *memFriends {
	return &memFriends{pairs: map[string]bool{"alice|bob": true}}
}

// ─── 一个 inbox spy 记录入队事件 ─────────────────────────────────────

type spyInbox struct {
	mu          sync.Mutex
	messages    []string // "toAgent|messageID"
	artifacts   []string // "toAgent|artifactID"
	transitions []string // "toAgent|taskID|from→to"
}

func newSpyInbox() *spyInbox { return &spyInbox{} }

func (s *spyInbox) EnqueueMessage(_ context.Context, to string, m *Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.messages = append(s.messages, to+"|"+m.MessageID)
	return nil
}

func (s *spyInbox) EnqueueArtifact(_ context.Context, to string, a *Artifact) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.artifacts = append(s.artifacts, to+"|"+a.ArtifactID)
	return nil
}

func (s *spyInbox) EnqueueTransition(_ context.Context, to, taskID string, from, to2 State, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.transitions = append(s.transitions, to+"|"+taskID+"|"+string(from)+"→"+string(to2))
	return nil
}

func (s *spyInbox) EnqueueTimelineUpdate(_ context.Context, _ string, _ TimelineUpdateInput) error {
	return nil
}

// ─── Submit tests ───────────────────────────────────────────────────

func submitInputOK() SubmitInput {
	return SubmitInput{
		TaskID:      "t-001",
		ContextID:   "",
		FromAgentID: "alice",
		ToAgentID:   "bob",
		CallerUID:   1,
		Message: Message{
			MessageID: "m-001",
			Parts:     []Part{{Kind: PartText, Text: "hi"}},
		},
	}
}

func TestSubmit_HappyPath(t *testing.T) {
	repo := newMemRepo()
	inbox := newSpyInbox()
	svc := NewService(repo, defaultAgents(), defaultFriends()).WithInbox(inbox)

	created, err := svc.Submit(context.Background(), submitInputOK())
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if created.Status != StateSubmitted {
		t.Fatalf("status: %v", created.Status)
	}
	if created.ContextID != "t-001" { // 自动用 task_id 作 context
		t.Fatalf("context: %q", created.ContextID)
	}
	if len(inbox.messages) != 1 || inbox.messages[0] != "bob|m-001" {
		t.Fatalf("inbox: %+v", inbox.messages)
	}
}

func TestSubmit_Idempotent(t *testing.T) {
	repo := newMemRepo()
	svc := NewService(repo, defaultAgents(), defaultFriends())

	_, err := svc.Submit(context.Background(), submitInputOK())
	if err != nil {
		t.Fatal(err)
	}
	// 相同 task_id 再提交：memRepo 会返回已有 task（幂等）
	_, err = svc.Submit(context.Background(), submitInputOK())
	if err != nil {
		t.Fatalf("idempotent submit: %v", err)
	}
}

func TestSubmit_RejectsNotOwner(t *testing.T) {
	svc := NewService(newMemRepo(), defaultAgents(), defaultFriends())
	in := submitInputOK()
	in.CallerUID = 99 // 不是 alice 的 owner
	_, err := svc.Submit(context.Background(), in)
	if !errors.Is(err, ErrNotParticipant) {
		t.Fatalf("want ErrNotParticipant, got %v", err)
	}
}

func TestSubmit_RejectsToVirtualUser(t *testing.T) {
	svc := NewService(newMemRepo(), defaultAgents(), defaultFriends())
	in := submitInputOK()
	in.ToAgentID = "virtual-user-1"
	_, err := svc.Submit(context.Background(), in)
	if err == nil {
		t.Fatal("want virtual-user rejection")
	}
}

func TestSubmit_RejectsWhenNotFriends(t *testing.T) {
	svc := NewService(newMemRepo(), defaultAgents(), &memFriends{pairs: nil})
	_, err := svc.Submit(context.Background(), submitInputOK())
	if err == nil {
		t.Fatal("want not-friends rejection")
	}
}

func TestSubmit_RejectsSelfFriend(t *testing.T) {
	svc := NewService(newMemRepo(), defaultAgents(), defaultFriends())
	in := submitInputOK()
	in.ToAgentID = "alice"
	_, err := svc.Submit(context.Background(), in)
	if err == nil {
		t.Fatal("want self rejection")
	}
}

func TestSubmit_BadTaskID(t *testing.T) {
	svc := NewService(newMemRepo(), defaultAgents(), defaultFriends())
	in := submitInputOK()
	in.TaskID = "a b" // 空格
	_, err := svc.Submit(context.Background(), in)
	if !errors.Is(err, ErrInvalidTaskID) {
		t.Fatalf("want ErrInvalidTaskID, got %v", err)
	}
}

// ─── AppendMessage tests ───────────────────────────────────────────

func TestAppendMessage_ByBob_RoleAgent(t *testing.T) {
	repo := newMemRepo()
	inbox := newSpyInbox()
	svc := NewService(repo, defaultAgents(), defaultFriends()).WithInbox(inbox)
	_, _ = svc.Submit(context.Background(), submitInputOK())

	got, err := svc.AppendMessage(context.Background(), AppendMessageInput{
		TaskID: "t-001", CallerAgent: "bob", CallerUID: 2,
		MessageID: "m-002", Parts: []Part{{Kind: PartText, Text: "need more info"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Role != RoleAgent {
		t.Fatalf("role should be agent when bob appends, got %v", got.Role)
	}
	// 消息进 alice 的 inbox
	if len(inbox.messages) != 2 || inbox.messages[1] != "alice|m-002" {
		t.Fatalf("inbox: %+v", inbox.messages)
	}
}

func TestAppendMessage_ByAlice_RoleUser(t *testing.T) {
	repo := newMemRepo()
	svc := NewService(repo, defaultAgents(), defaultFriends())
	_, _ = svc.Submit(context.Background(), submitInputOK())

	got, err := svc.AppendMessage(context.Background(), AppendMessageInput{
		TaskID: "t-001", CallerAgent: "alice", CallerUID: 1,
		MessageID: "m-002", Parts: []Part{{Kind: PartText, Text: "more context"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Role != RoleUser {
		t.Fatalf("role: %v", got.Role)
	}
}

func TestAppendMessage_NotParticipant(t *testing.T) {
	repo := newMemRepo()
	agents := defaultAgents()
	agents["charlie"] = struct {
		ownerUID int64
		kind     string
	}{ownerUID: 3, kind: "normal"}
	svc := NewService(repo, agents, defaultFriends())
	_, _ = svc.Submit(context.Background(), submitInputOK())

	_, err := svc.AppendMessage(context.Background(), AppendMessageInput{
		TaskID: "t-001", CallerAgent: "charlie", CallerUID: 3,
		MessageID: "m-002", Parts: []Part{{Kind: PartText, Text: "hi"}},
	})
	if !errors.Is(err, ErrNotParticipant) {
		t.Fatalf("want ErrNotParticipant, got %v", err)
	}
}

func TestAppendMessage_RejectsOnTerminalTask(t *testing.T) {
	repo := newMemRepo()
	svc := NewService(repo, defaultAgents(), defaultFriends())
	_, _ = svc.Submit(context.Background(), submitInputOK())
	// 让 bob 转 working → completed
	_, _ = svc.Transition(context.Background(), TransitionInput{
		TaskID: "t-001", CallerAgent: "bob", CallerUID: 2, ToState: StateWorking,
	})
	_, _ = svc.Transition(context.Background(), TransitionInput{
		TaskID: "t-001", CallerAgent: "bob", CallerUID: 2, ToState: StateCompleted,
	})

	_, err := svc.AppendMessage(context.Background(), AppendMessageInput{
		TaskID: "t-001", CallerAgent: "bob", CallerUID: 2,
		MessageID: "m-002", Parts: []Part{{Kind: PartText, Text: "post-completion"}},
	})
	if err == nil {
		t.Fatal("want terminal rejection")
	}
}

// ─── AppendArtifact tests ──────────────────────────────────────────

func TestAppendArtifact_OnlyByServingAgent(t *testing.T) {
	repo := newMemRepo()
	svc := NewService(repo, defaultAgents(), defaultFriends())
	_, _ = svc.Submit(context.Background(), submitInputOK())

	// alice 不能 append artifact
	_, err := svc.AppendArtifact(context.Background(), AppendArtifactInput{
		TaskID: "t-001", CallerAgent: "alice", CallerUID: 1,
		ArtifactID: "a-1", Parts: []Part{{Kind: PartText, Text: "x"}},
	})
	if !errors.Is(err, ErrNotParticipant) {
		t.Fatalf("alice append: want ErrNotParticipant, got %v", err)
	}

	// bob 可以
	got, err := svc.AppendArtifact(context.Background(), AppendArtifactInput{
		TaskID: "t-001", CallerAgent: "bob", CallerUID: 2,
		ArtifactID: "a-1", Parts: []Part{{Kind: PartText, Text: "result"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.ArtifactID != "a-1" {
		t.Fatal("artifact id")
	}
}

func TestAppendArtifact_DuplicateWithinTask(t *testing.T) {
	repo := newMemRepo()
	svc := NewService(repo, defaultAgents(), defaultFriends())
	_, _ = svc.Submit(context.Background(), submitInputOK())

	_, _ = svc.AppendArtifact(context.Background(), AppendArtifactInput{
		TaskID: "t-001", CallerAgent: "bob", CallerUID: 2,
		ArtifactID: "a-1", Parts: []Part{{Kind: PartText, Text: "v1"}},
	})
	_, err := svc.AppendArtifact(context.Background(), AppendArtifactInput{
		TaskID: "t-001", CallerAgent: "bob", CallerUID: 2,
		ArtifactID: "a-1", Parts: []Part{{Kind: PartText, Text: "v2"}},
	})
	if !errors.Is(err, ErrArtifactIDDuplicate) {
		t.Fatalf("want ErrArtifactIDDuplicate, got %v", err)
	}
}

// ─── Transition tests ──────────────────────────────────────────────

func TestTransition_BobDrivesWorkingFlow(t *testing.T) {
	repo := newMemRepo()
	inbox := newSpyInbox()
	svc := NewService(repo, defaultAgents(), defaultFriends()).WithInbox(inbox)
	_, _ = svc.Submit(context.Background(), submitInputOK())

	// submitted → working
	_, err := svc.Transition(context.Background(), TransitionInput{
		TaskID: "t-001", CallerAgent: "bob", CallerUID: 2, ToState: StateWorking,
	})
	if err != nil {
		t.Fatalf("→working: %v", err)
	}

	// working → completed
	got, err := svc.Transition(context.Background(), TransitionInput{
		TaskID: "t-001", CallerAgent: "bob", CallerUID: 2, ToState: StateCompleted,
	})
	if err != nil {
		t.Fatalf("→completed: %v", err)
	}
	if got.Status != StateCompleted {
		t.Fatalf("final: %v", got.Status)
	}
	// 两次转换都入 alice inbox
	if len(inbox.transitions) != 2 {
		t.Fatalf("transitions: %+v", inbox.transitions)
	}
}

func TestTransition_AliceCanOnlyCancel(t *testing.T) {
	repo := newMemRepo()
	svc := NewService(repo, defaultAgents(), defaultFriends())
	_, _ = svc.Submit(context.Background(), submitInputOK())

	// alice 试图转 working → 拒
	_, err := svc.Transition(context.Background(), TransitionInput{
		TaskID: "t-001", CallerAgent: "alice", CallerUID: 1, ToState: StateWorking,
	})
	if err == nil || !contains(err.Error(), "only serving") {
		t.Fatalf("alice →working: want serving rejection, got %v", err)
	}

	// alice 可以 cancel
	_, err = svc.Transition(context.Background(), TransitionInput{
		TaskID: "t-001", CallerAgent: "alice", CallerUID: 1, ToState: StateCanceled,
	})
	if err != nil {
		t.Fatalf("alice →canceled: %v", err)
	}
}

func TestTransition_InputRequiredRoundtrip(t *testing.T) {
	repo := newMemRepo()
	svc := NewService(repo, defaultAgents(), defaultFriends())
	_, _ = svc.Submit(context.Background(), submitInputOK())

	// bob → working → input-required
	_, _ = svc.Transition(context.Background(), TransitionInput{
		TaskID: "t-001", CallerAgent: "bob", CallerUID: 2, ToState: StateWorking,
	})
	_, err := svc.Transition(context.Background(), TransitionInput{
		TaskID: "t-001", CallerAgent: "bob", CallerUID: 2, ToState: StateInputRequired,
	})
	if err != nil {
		t.Fatal(err)
	}

	// alice 补消息 + 转回 submitted
	_, _ = svc.AppendMessage(context.Background(), AppendMessageInput{
		TaskID: "t-001", CallerAgent: "alice", CallerUID: 1,
		MessageID: "m-002", Parts: []Part{{Kind: PartText, Text: "clarification"}},
	})
	got, err := svc.Transition(context.Background(), TransitionInput{
		TaskID: "t-001", CallerAgent: "alice", CallerUID: 1, ToState: StateSubmitted,
	})
	if err != nil {
		t.Fatalf("alice → submitted: %v", err)
	}
	if got.Status != StateSubmitted {
		t.Fatalf("after re-submit: %v", got.Status)
	}

	// bob 继续 working → completed
	_, _ = svc.Transition(context.Background(), TransitionInput{
		TaskID: "t-001", CallerAgent: "bob", CallerUID: 2, ToState: StateWorking,
	})
	final, _ := svc.Transition(context.Background(), TransitionInput{
		TaskID: "t-001", CallerAgent: "bob", CallerUID: 2, ToState: StateCompleted,
	})
	if final.Status != StateCompleted {
		t.Fatalf("final: %v", final.Status)
	}
}

func TestTransition_InvalidTransitionReturnsError(t *testing.T) {
	repo := newMemRepo()
	svc := NewService(repo, defaultAgents(), defaultFriends())
	_, _ = svc.Submit(context.Background(), submitInputOK())

	// submitted → completed 非法（必须先 working）
	_, err := svc.Transition(context.Background(), TransitionInput{
		TaskID: "t-001", CallerAgent: "bob", CallerUID: 2, ToState: StateCompleted,
	})
	if !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("want ErrInvalidTransition, got %v", err)
	}
}

// ─── Get / List tests ─────────────────────────────────────────────

func TestGet_ParticipantOnly(t *testing.T) {
	repo := newMemRepo()
	agents := defaultAgents()
	agents["charlie"] = struct {
		ownerUID int64
		kind     string
	}{ownerUID: 3, kind: "normal"}
	svc := NewService(repo, agents, defaultFriends())
	_, _ = svc.Submit(context.Background(), submitInputOK())

	// charlie 不是 participant
	_, _, _, err := svc.Get(context.Background(), "charlie", 3, "t-001", false, false)
	if !errors.Is(err, ErrNotParticipant) {
		t.Fatalf("charlie: want ErrNotParticipant, got %v", err)
	}

	// alice 可以拿
	_, _, _, err = svc.Get(context.Background(), "alice", 1, "t-001", true, true)
	if err != nil {
		t.Fatal(err)
	}
}

func TestListByContext_FilterByParticipant(t *testing.T) {
	repo := newMemRepo()
	svc := NewService(repo, defaultAgents(), defaultFriends())
	// alice → bob
	_, _ = svc.Submit(context.Background(), submitInputOK())

	got, err := svc.ListByContext(context.Background(), "alice", 1, "t-001")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d", len(got))
	}
}

// ─── helper ────────────────────────────────────────────────────────

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (indexOf(s, sub) >= 0)
}
func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
