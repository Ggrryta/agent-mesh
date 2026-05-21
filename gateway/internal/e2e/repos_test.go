package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/Ggrryta/agent-mesh/gateway/internal/domain/agent"
	"github.com/Ggrryta/agent-mesh/gateway/internal/domain/apikey"
	"github.com/Ggrryta/agent-mesh/gateway/internal/domain/friendship"
	"github.com/Ggrryta/agent-mesh/gateway/internal/domain/inbox"
	"github.com/Ggrryta/agent-mesh/gateway/internal/domain/skill"
	"github.com/Ggrryta/agent-mesh/gateway/internal/domain/task"
	"github.com/Ggrryta/agent-mesh/gateway/internal/domain/user"
)

// ─── memUserRepo ─────────────────────────────────────────────────

type memUserRepo struct {
	mu    sync.Mutex
	users map[int64]*user.User
	next  int64
}

func newMemUserRepo() *memUserRepo { return &memUserRepo{users: map[int64]*user.User{}} }

func (r *memUserRepo) Seed(uid int64, username string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.users[uid] = &user.User{ID: uid, Username: username, PasswordHash: "x", VirtualUserAgentID: user.VirtualAgentIDFor(uid)}
	if uid > r.next {
		r.next = uid
	}
}

func (r *memUserRepo) CreateWithVirtualAgent(_ context.Context, username, hash string) (*user.User, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.next++
	u := &user.User{ID: r.next, Username: username, PasswordHash: hash, VirtualUserAgentID: user.VirtualAgentIDFor(r.next)}
	r.users[u.ID] = u
	return u, nil
}

func (r *memUserRepo) GetByUsername(_ context.Context, username string) (*user.User, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, u := range r.users {
		if u.Username == username {
			return u, nil
		}
	}
	return nil, user.ErrUserNotFound
}

func (r *memUserRepo) GetByID(_ context.Context, id int64) (*user.User, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	u, ok := r.users[id]
	if !ok {
		return nil, user.ErrUserNotFound
	}
	return u, nil
}

// ─── memAgentRepo ────────────────────────────────────────────────

type memAgentRepo struct {
	mu   sync.Mutex
	byID map[string]*agent.Agent
}

func newMemAgentRepo() *memAgentRepo { return &memAgentRepo{byID: map[string]*agent.Agent{}} }

func (r *memAgentRepo) Create(_ context.Context, a *agent.Agent) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.byID[a.AgentID]; ok {
		return agent.ErrAgentIDExists
	}
	cp := *a
	r.byID[a.AgentID] = &cp
	return nil
}

func (r *memAgentRepo) Upsert(_ context.Context, a *agent.Agent) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := *a
	r.byID[a.AgentID] = &cp
	return nil
}

func (r *memAgentRepo) GetByAgentID(_ context.Context, id string) (*agent.Agent, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	a, ok := r.byID[id]
	if !ok {
		return nil, agent.ErrAgentNotFound
	}
	cp := *a
	return &cp, nil
}

func (r *memAgentRepo) UpdateStatus(_ context.Context, id string, s agent.Status) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	a, ok := r.byID[id]
	if !ok {
		return agent.ErrAgentNotFound
	}
	a.Status = s
	return nil
}

func (r *memAgentRepo) UpdateHeartbeat(_ context.Context, id string, ts time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	a, ok := r.byID[id]
	if !ok {
		return agent.ErrAgentNotFound
	}
	a.LastHeartbeatAt = &ts
	return nil
}

func (r *memAgentRepo) Delete(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.byID, id)
	return nil
}

func (r *memAgentRepo) List(_ context.Context, f agent.Filter) ([]*agent.Agent, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*agent.Agent, 0)
	for _, a := range r.byID {
		if f.Status != "" && a.Status != f.Status {
			continue
		}
		cp := *a
		out = append(out, &cp)
	}
	return out, nil
}

// ─── memFriendRepo ───────────────────────────────────────────────

type memFriendRepo struct {
	mu    sync.Mutex
	pairs map[string]bool
}

func newMemFriendRepo() *memFriendRepo { return &memFriendRepo{pairs: map[string]bool{}} }

func (r *memFriendRepo) Seed(from, to string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pairs[from+"|"+to] = true
	r.pairs[to+"|"+from] = true
}

func (r *memFriendRepo) GetByPair(_ context.Context, from, to string) (*friendship.Friendship, error) {
	return nil, friendship.ErrNotFound
}

func (r *memFriendRepo) GetByID(_ context.Context, id int64) (*friendship.Friendship, error) {
	return nil, friendship.ErrNotFound
}

func (r *memFriendRepo) Insert(_ context.Context, from, to, reason string) (*friendship.Friendship, error) {
	return &friendship.Friendship{ID: 1, FromAgentID: from, ToAgentID: to, Status: friendship.StatusPending}, nil
}

func (r *memFriendRepo) UpdateToPending(_ context.Context, id int64, reason string) (bool, error) {
	return true, nil
}

func (r *memFriendRepo) UpdateStatus(_ context.Context, id int64, from, to friendship.Status) (bool, error) {
	return true, nil
}

func (r *memFriendRepo) ListInvolvingAgent(_ context.Context, agentID string) ([]*friendship.Friendship, error) {
	return nil, nil
}

func (r *memFriendRepo) ListIncomingPending(_ context.Context, agentID string) ([]*friendship.Friendship, error) {
	return nil, nil
}

func (r *memFriendRepo) ExistsAccepted(_ context.Context, a, b string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.pairs[a+"|"+b], nil
}

// ─── memInboxRepo ────────────────────────────────────────────────

type memInboxRepo struct {
	mu     sync.Mutex
	rows   map[int64]*inbox.Event
	nextID int64
}

func newMemInboxRepo() *memInboxRepo { return &memInboxRepo{rows: map[int64]*inbox.Event{}} }

func (r *memInboxRepo) Insert(_ context.Context, e *inbox.Event) (*inbox.Event, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.nextID++
	cp := *e
	cp.ID = r.nextID
	cp.CreatedAt = time.Now()
	r.rows[cp.ID] = &cp
	result := cp
	return &result, nil
}

func (r *memInboxRepo) ListSince(_ context.Context, agentID string, sinceID int64, limit int) ([]*inbox.Event, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*inbox.Event, 0)
	for id := sinceID + 1; id <= r.nextID; id++ {
		if len(out) >= limit {
			break
		}
		e, ok := r.rows[id]
		if !ok || e.AgentID != agentID {
			continue
		}
		cp := *e
		out = append(out, &cp)
	}
	return out, nil
}

func (r *memInboxRepo) MarkDelivered(_ context.Context, ids []int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	for _, id := range ids {
		if e, ok := r.rows[id]; ok {
			e.DeliveredAt = &now
		}
	}
	return nil
}

// ─── memTaskRepo ─────────────────────────────────────────────────

type memTaskRepo struct {
	mu        sync.Mutex
	tasks     map[string]*task.Task
	messages  map[string][]*task.Message
	artifacts map[string][]*task.Artifact
	msgByID   map[string]*task.Message
}

func newMemTaskRepo() *memTaskRepo {
	return &memTaskRepo{
		tasks:     map[string]*task.Task{},
		messages:  map[string][]*task.Message{},
		artifacts: map[string][]*task.Artifact{},
		msgByID:   map[string]*task.Message{},
	}
}

func (r *memTaskRepo) CreateTask(_ context.Context, t *task.Task, firstMsg *task.Message) (*task.Task, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if existing, ok := r.tasks[t.TaskID]; ok {
		return existing, nil
	}
	now := time.Now()
	t.CreatedAt = now
	t.UpdatedAt = now
	cp := *t
	r.tasks[t.TaskID] = &cp

	if firstMsg != nil {
		firstMsg.CreatedAt = now
		mcp := *firstMsg
		r.messages[t.TaskID] = append(r.messages[t.TaskID], &mcp)
		r.msgByID[firstMsg.MessageID] = &mcp
	}
	result := cp
	return &result, nil
}

func (r *memTaskRepo) AppendMessage(_ context.Context, m *task.Message) (*task.Message, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if existing, ok := r.msgByID[m.MessageID]; ok {
		return existing, nil
	}
	m.CreatedAt = time.Now()
	cp := *m
	r.messages[m.TaskID] = append(r.messages[m.TaskID], &cp)
	r.msgByID[m.MessageID] = &cp
	return &cp, nil
}

func (r *memTaskRepo) AppendArtifact(_ context.Context, a *task.Artifact) (*task.Artifact, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, existing := range r.artifacts[a.TaskID] {
		if existing.ArtifactID == a.ArtifactID {
			return nil, task.ErrArtifactIDDuplicate
		}
	}
	a.CreatedAt = time.Now()
	cp := *a
	r.artifacts[a.TaskID] = append(r.artifacts[a.TaskID], &cp)
	return &cp, nil
}

func (r *memTaskRepo) TransitionStatus(_ context.Context, taskID string, fromStates []task.State, to task.State, statusMsg, errorMsg string) (bool, *task.Task, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	t, ok := r.tasks[taskID]
	if !ok {
		return false, nil, task.ErrTaskNotFound
	}
	allowed := false
	for _, s := range fromStates {
		if t.Status == s {
			allowed = true
			break
		}
	}
	if !allowed {
		cp := *t
		return false, &cp, nil
	}
	t.Status = to
	t.StatusMessage = statusMsg
	t.ErrorMsg = errorMsg
	t.UpdatedAt = time.Now()
	cp := *t
	return true, &cp, nil
}

func (r *memTaskRepo) GetTask(_ context.Context, taskID string, withHistory, withArtifacts bool) (*task.Task, []*task.Message, []*task.Artifact, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	t, ok := r.tasks[taskID]
	if !ok {
		return nil, nil, nil, task.ErrTaskNotFound
	}
	cp := *t
	var msgs []*task.Message
	var arts []*task.Artifact
	if withHistory {
		msgs = r.messages[taskID]
	}
	if withArtifacts {
		arts = r.artifacts[taskID]
	}
	return &cp, msgs, arts, nil
}

func (r *memTaskRepo) ListByContext(_ context.Context, contextID string) ([]*task.Task, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*task.Task, 0)
	for _, t := range r.tasks {
		if t.ContextID == contextID {
			cp := *t
			out = append(out, &cp)
		}
	}
	return out, nil
}

func (r *memTaskRepo) GetMessageByID(_ context.Context, messageID string) (*task.Message, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	m, ok := r.msgByID[messageID]
	if !ok {
		return nil, task.ErrMessageNotFound
	}
	cp := *m
	return &cp, nil
}

func (r *memTaskRepo) ListTimeline(_ context.Context, contextID string, sinceID int64, limit int) ([]*task.TimelineEntry, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if limit <= 0 {
		limit = 100
	}
	out := make([]*task.TimelineEntry, 0)
	// 收集所有 message + artifact entries（按 created_at 排序）
	type item struct {
		t   time.Time
		entry *task.TimelineEntry
	}
	items := make([]item, 0)
	idCounter := int64(0)
	for tid, msgs := range r.messages {
		_ = tid
		for _, m := range msgs {
			idCounter++
			if idCounter <= sinceID {
				continue
			}
			if m.ContextID != contextID {
				continue
			}
			items = append(items, item{
				t: m.CreatedAt,
				entry: &task.TimelineEntry{
					Kind:      "message",
					EntryID:   idCounter,
					TaskID:    m.TaskID,
					ContextID: m.ContextID,
					RefID:     m.MessageID,
					From:      string(m.Role),
					Preview:   m.Preview,
					CreatedAt: m.CreatedAt,
				},
			})
		}
	}
	for _, arts := range r.artifacts {
		for _, a := range arts {
			idCounter++
			if idCounter <= sinceID {
				continue
			}
			if a.ContextID != contextID {
				continue
			}
			items = append(items, item{
				t: a.CreatedAt,
				entry: &task.TimelineEntry{
					Kind:      "artifact",
					EntryID:   idCounter,
					TaskID:    a.TaskID,
					ContextID: a.ContextID,
					RefID:     a.ArtifactID,
					Name:      a.Name,
					CreatedAt: a.CreatedAt,
				},
			})
		}
	}
	// 简单按时间排序（n 小，不优化）
	for i := 0; i < len(items); i++ {
		for j := i + 1; j < len(items); j++ {
			if items[j].t.Before(items[i].t) {
				items[i], items[j] = items[j], items[i]
			}
		}
	}
	for _, it := range items {
		if len(out) >= limit {
			break
		}
		out = append(out, it.entry)
	}
	return out, nil
}

func (r *memTaskRepo) ListRecentByAgents(_ context.Context, _ []string, _ int) ([]*task.Task, error) {
	return nil, nil
}

func (r *memTaskRepo) UpdateChatStreak(_ context.Context, _ string, _ bool) error { return nil }

func (r *memTaskRepo) ListChatterTasks(_ context.Context, _ int, _ time.Time) ([]*task.Task, error) {
	return nil, nil
}

func (r *memTaskRepo) DeleteTerminalTasksBefore(_ context.Context, _ int64, _ time.Time) (int, error) {
	return 0, nil
}

func (r *memTaskRepo) DeleteTaskByID(_ context.Context, _ string) error { return nil }

func (r *memTaskRepo) TouchActivity(_ context.Context, _ string) error { return nil }

func (r *memTaskRepo) ListInactiveNonTerminal(_ context.Context, _ time.Time) ([]*task.Task, error) { return nil, nil }

// ─── noopSkillRepo ───────────────────────────────────────────────

type noopSkillRepo struct{}

func (r *noopSkillRepo) ReplaceByAgentID(_ context.Context, _ string, _ []skill.Input) error {
	return nil
}
func (r *noopSkillRepo) ListByAgentID(_ context.Context, _ string) ([]*skill.Skill, error) {
	return nil, nil
}
func (r *noopSkillRepo) ListByAgentIDs(_ context.Context, _ []string) (map[string][]*skill.Skill, error) {
	return map[string][]*skill.Skill{}, nil
}

// ─── memAPIKeyRepo ───────────────────────────────────────────────

type memAPIKeyRepo struct {
	mu     sync.Mutex
	byID   map[int64]*apikey.Key
	byHash map[string]int64
	next   int64
}

func newMemAPIKeyRepo() *memAPIKeyRepo {
	return &memAPIKeyRepo{byID: map[int64]*apikey.Key{}, byHash: map[string]int64{}}
}

func (r *memAPIKeyRepo) Insert(_ context.Context, uid int64, hash, prefix, label string) (*apikey.Key, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.next++
	k := &apikey.Key{ID: r.next, OwnerUID: uid, KeyPrefix: prefix, Label: label, CreatedAt: time.Now()}
	r.byID[k.ID] = k
	r.byHash[hash] = k.ID
	return k, nil
}

func (r *memAPIKeyRepo) FindByHash(_ context.Context, hash string) (*apikey.Key, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	id, ok := r.byHash[hash]
	if !ok {
		return nil, apikey.ErrKeyNotFound
	}
	cp := *r.byID[id]
	return &cp, nil
}

func (r *memAPIKeyRepo) ListByOwner(_ context.Context, uid int64) ([]*apikey.Key, error) {
	return nil, nil
}

func (r *memAPIKeyRepo) Revoke(_ context.Context, uid, id int64) error { return nil }

func (r *memAPIKeyRepo) TouchLastUsed(_ context.Context, id int64, ts time.Time) error { return nil }

// 保留 import
var (
	_ = fmt.Sprintf
	_ = json.Marshal
)
