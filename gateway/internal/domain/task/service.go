package task

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// AgentLookup 是 service 所需的 agent 元数据视图。
// 实现在 domain/agent（NewLookupAdapter），复用 friendship 用的接口思路。
type AgentLookup interface {
	Lookup(ctx context.Context, agentID string) (ownerUID int64, kind string, found bool)
}

// FriendshipChecker 是 service 所需的好友关系视图。
type FriendshipChecker interface {
	AreFriends(ctx context.Context, a, b string) (bool, error)
}

// GroupChecker 判断两个 agent 是否在同一群组，以及提供群组 fan-out 所需的查询。
// 群组成员之间可直接通信（即使不是好友），详见群组协作设计。
type GroupChecker interface {
	SameGroup(ctx context.Context, a, b string) (bool, error)
	// MembersOfGroupsContaining 返回所有与 agent 共处同一群组的其他 agent ID（去重）。
	// 用于 timeline_update fan-out：给所有"看得到"但非直接参与者的 agent 推元数据。
	MembersOfGroupsContaining(ctx context.Context, agentID string) ([]string, error)
}

// kindVirtualUser 表示 virtual-user agent 的 kind。
// 定义在本包以避免 import domain/agent 造成循环。
const kindVirtualUser = "virtual-user"

// Service 封装 Task 业务规则。
//
// 重要定位（详见 ADR 002）：
//   - Gateway 不"执行"任务；Service 的写路径只做"持久化 + 入 inbox"
//   - 所有状态变更都是 agent 主动汇报的；Service 只做合法性校验
//   - inbox 入队是 service 的责任（通过 Inboxer 接口），但具体实现不关心
type Service struct {
	repo    Repo
	agents  AgentLookup
	friends FriendshipChecker
	groups  GroupChecker // 可为 nil：没装配群组时退化为只查 friendship
	inbox   Inboxer      // 可为 nil：没 inbox 时跳过入队（启动阶段或测试）
	outbox  OutboxWriter // 可为 nil：没 outbox 时走旧路径（直接调 inbox）
	kafka   KafkaPublisher // 可为 nil：乐观直发 Kafka（Dispatcher 兜底）
}

// KafkaPublisher 乐观直发接口。写完 outbox 后立即尝试发 Kafka，失败不影响正确性。
type KafkaPublisher interface {
	Publish(ctx context.Context, topic, key string, value []byte) error
}

// OutboxWriter 是 Transactional Outbox 的写入接口。
type OutboxWriter interface {
	Insert(ctx context.Context, eventType string, payload []byte) error
	MarkSentByEventType(ctx context.Context, eventType string, payload []byte) error
}

// Inboxer 是 service 用来派发事件的最小接口。
// 具体实现在 domain/inbox 包；放这里为了单测方便。
type Inboxer interface {
	EnqueueMessage(ctx context.Context, toAgentID string, m *Message) error
	EnqueueArtifact(ctx context.Context, toAgentID string, a *Artifact) error
	EnqueueTransition(ctx context.Context, toAgentID, taskID string, from, to State, statusMessage string) error
	EnqueueTimelineUpdate(ctx context.Context, toAgentID string, p TimelineUpdateInput) error
}

// TimelineUpdateInput 是 Service → Inboxer 传 timeline 更新元数据的形式。
// 放 task 包避免 inbox ↔ task 循环依赖。
type TimelineUpdateInput struct {
	ContextID string
	EntryKind string
	TaskID    string
	RefID     string
	From      string
	Preview   string
	Name      string
}

// NewService 装配 service。inbox 可为 nil。
func NewService(repo Repo, agents AgentLookup, friends FriendshipChecker) *Service {
	if agents == nil || friends == nil {
		panic("task: agents and friends are required")
	}
	return &Service{repo: repo, agents: agents, friends: friends}
}

// WithInbox 注入 inbox 依赖；nil 表示禁用入队。
func (s *Service) WithInbox(inbox Inboxer) *Service {
	s.inbox = inbox
	return s
}

// WithOutbox 注入 Transactional Outbox 写入器。
func (s *Service) WithOutbox(o OutboxWriter) *Service {
	s.outbox = o
	return s
}

// WithKafka 注入 Kafka 直发能力（乐观投递，Dispatcher 兜底）。
func (s *Service) WithKafka(k KafkaPublisher) *Service {
	s.kafka = k
	return s
}

// WithGroups 注入群组检查器。装配后鉴权变为 "是好友 OR 同群"。
func (s *Service) WithGroups(g GroupChecker) *Service {
	s.groups = g
	return s
}

// canCommunicate 判断 a 能否与 b 通信：
// 1. 显式好友关系（friendship.accepted）
// 2. 同一个群组（若装配了 GroupChecker）
// 任一成立即可。
func (s *Service) canCommunicate(ctx context.Context, a, b string) (bool, error) {
	ok, err := s.friends.AreFriends(ctx, a, b)
	if err != nil {
		return false, err
	}
	if ok {
		return true, nil
	}
	if s.groups == nil {
		return false, nil
	}
	return s.groups.SameGroup(ctx, a, b)
}

// ─── 写入：提交新 Task ─────────────────────────────────────────────────

// SubmitInput 是 Submit 的入参。
type SubmitInput struct {
	TaskID      string // 客户端指定（通常 UUID）；空则报错
	ContextID   string // 空则 = TaskID（新 context）
	FromAgentID string // 发起方
	ToAgentID   string // 被叫方
	CallerUID   int64  // JWT 里的 UID，service 用来验 from_agent_id 归属
	Message     Message
}

// Submit 提交一个新 Task。
//
// 执行顺序：
//  1. 校验 from 属于 caller + from/to 都不是 virtual-user（但 to 可以是
//     virtual-user？按 ADR 002 的设计，任务不反抛给用户，所以 to 必须非 virtual）
//  2. AreFriends(from, to)
//  3. 首条 message 格式校验
//  4. CreateTask（事务写主表 + 首条 message）
//  5. inbox 入队：给 ToAgentID 推一条 message 事件
//
// 注意：inbox 入队失败**不回滚**，只打日志。消息已经在 DB 里，被叫方可以
// 主动拉 GET /tasks/{id} 或 GET /inbox 补。
func (s *Service) Submit(ctx context.Context, in SubmitInput) (*Task, error) {
	// 归一化
	in.TaskID = normalizeID(in.TaskID)
	in.ContextID = normalizeID(in.ContextID)
	in.FromAgentID = strings.TrimSpace(in.FromAgentID)
	in.ToAgentID = strings.TrimSpace(in.ToAgentID)
	in.Message.MessageID = normalizeID(in.Message.MessageID)

	if in.ContextID == "" {
		in.ContextID = in.TaskID
	}

	// 基础格式
	if err := ValidateTaskID(in.TaskID); err != nil {
		return nil, err
	}
	if err := ValidateContextID(in.ContextID); err != nil {
		return nil, err
	}
	if err := ValidateMessageID(in.Message.MessageID); err != nil {
		return nil, err
	}
	if in.FromAgentID == "" || in.ToAgentID == "" {
		return nil, fmt.Errorf("task: from/to agent id required")
	}
	if in.FromAgentID == in.ToAgentID {
		return nil, fmt.Errorf("task: from and to must differ")
	}
	// Parts 必填
	if err := ValidateParts(in.Message.Parts); err != nil {
		return nil, err
	}

	// ownership + kind 校验
	fromOwner, fromKind, ok := s.agents.Lookup(ctx, in.FromAgentID)
	if !ok {
		return nil, fmt.Errorf("task: from agent not found")
	}
	if fromOwner != in.CallerUID {
		return nil, ErrNotParticipant
	}
	if fromKind == kindVirtualUser {
		// virtual-user 可作为发起方（用户下发任务）
		// 这里目前不拒；按 concepts 描述"用户通过 virtual-user-agent 下任务"
	}

	_, toKind, ok := s.agents.Lookup(ctx, in.ToAgentID)
	if !ok {
		return nil, fmt.Errorf("task: to agent not found")
	}
	if toKind == kindVirtualUser {
		// 关键约束：任务不反抛给用户。Gateway 拒绝 to 是 virtual-user 的 task。
		return nil, fmt.Errorf("task: to agent must not be virtual-user")
	}

	// Friendship
	friends, err := s.canCommunicate(ctx, in.FromAgentID, in.ToAgentID)
	if err != nil {
		return nil, fmt.Errorf("task: friend check: %w", err)
	}
	if !friends {
		return nil, fmt.Errorf("task: agents are not friends")
	}

	// 组装
	task := &Task{
		TaskID:      in.TaskID,
		ContextID:   in.ContextID,
		FromAgentID: in.FromAgentID,
		ToAgentID:   in.ToAgentID,
		Status:      StateSubmitted,
	}
	firstMsg := &Message{
		MessageID:  in.Message.MessageID,
		TaskID:     in.TaskID,
		ContextID:  in.ContextID,
		Role:       RoleUser, // 发起方的第一条消息永远是 user
		Parts:      in.Message.Parts,
		Metadata:   in.Message.Metadata,
		RefTaskIDs: in.Message.RefTaskIDs,
	}

	created, err := s.repo.CreateTask(ctx, task, firstMsg)
	if err != nil {
		return nil, err
	}

	// Inbox 入队（尽力而为）
	s.enqueueMessage(ctx, created.ToAgentID, created.FromAgentID, firstMsg)
	return created, nil
}

// ─── 写入：追加 message ────────────────────────────────────────────────

// AppendMessageInput 是 AppendMessage 的入参。
type AppendMessageInput struct {
	TaskID      string
	CallerAgent string // caller 的 agent_id（来自 JWT）
	CallerUID   int64
	MessageID   string
	Parts       []Part
	Preview     string // 可选：agent 自己决定的摘要，对群组旁观者公开
	Metadata    map[string]any
	RefTaskIDs  []string
}

// AppendMessage 向已存在的 Task 追加一条消息。
//
// Role 的决定：
//   - 如果 caller 是 task.FromAgentID 的 owner → role=user（发起方继续说话）
//   - 如果 caller 是 task.ToAgentID 的 owner  → role=agent（被叫方回复）
//   - 其它 → 拒绝（非 participant）
//
// 终态 Task 拒绝追加消息（completed/canceled/failed/rejected）。
func (s *Service) AppendMessage(ctx context.Context, in AppendMessageInput) (*Message, error) {
	if err := ValidateTaskID(in.TaskID); err != nil {
		return nil, err
	}
	if err := ValidateMessageID(in.MessageID); err != nil {
		return nil, err
	}
	if err := ValidateParts(in.Parts); err != nil {
		return nil, err
	}

	// 拿 task 主表判 participant + 状态
	t, _, _, err := s.repo.GetTask(ctx, in.TaskID, false, false)
	if err != nil {
		return nil, err
	}
	if t.Status.IsTerminal() {
		return nil, fmt.Errorf("task: cannot append message to terminal task (status=%s)", t.Status)
	}

	role, err := s.resolveRole(ctx, t, in.CallerAgent, in.CallerUID)
	if err != nil {
		return nil, err
	}

	m := &Message{
		MessageID:  in.MessageID,
		TaskID:     t.TaskID,
		ContextID:  t.ContextID,
		Role:       role,
		Parts:      in.Parts,
		Preview:    in.Preview,
		Metadata:   in.Metadata,
		RefTaskIDs: in.RefTaskIDs,
	}
	saved, err := s.repo.AppendMessage(ctx, m)
	if err != nil {
		return nil, err
	}

	// ── chat_score：更新闲聊连击计数（auto-close 兜底用）──
	msgText := extractPartsText(in.Parts)
	chatScore := ComputeChatScore(msgText)
	if chatScore >= ChatScoreThreshold {
		_ = s.repo.UpdateChatStreak(ctx, t.TaskID, true) // increment
	} else {
		_ = s.repo.UpdateChatStreak(ctx, t.TaskID, false) // reset
	}

	// 入对方 inbox
	peer := t.peerOf(in.CallerAgent)
	s.enqueueMessage(ctx, peer, in.CallerAgent, saved)
	return saved, nil
}

// ─── 写入：追加 artifact ──────────────────────────────────────────────

// AppendArtifactInput 是 AppendArtifact 的入参。
type AppendArtifactInput struct {
	TaskID      string
	CallerAgent string
	CallerUID   int64
	ArtifactID  string
	Name        string
	Description string
	Parts       []Part
	Metadata    map[string]any
}

// AppendArtifact 只允许**被叫方**（ToAgentID 的 owner）调用。
// 发起方不产出 artifact —— 按 A2A 语义，artifact 是"被请求方的交付物"。
func (s *Service) AppendArtifact(ctx context.Context, in AppendArtifactInput) (*Artifact, error) {
	if err := ValidateTaskID(in.TaskID); err != nil {
		return nil, err
	}
	if err := ValidateArtifactID(in.ArtifactID); err != nil {
		return nil, err
	}
	if err := ValidateParts(in.Parts); err != nil {
		return nil, err
	}

	t, _, _, err := s.repo.GetTask(ctx, in.TaskID, false, false)
	if err != nil {
		return nil, err
	}
	if t.Status.IsTerminal() {
		return nil, fmt.Errorf("task: cannot append artifact to terminal task (status=%s)", t.Status)
	}

	// 必须是 ToAgentID 的 owner
	if in.CallerAgent != t.ToAgentID {
		return nil, ErrNotParticipant
	}
	owner, _, ok := s.agents.Lookup(ctx, in.CallerAgent)
	if !ok || owner != in.CallerUID {
		return nil, ErrNotParticipant
	}

	a := &Artifact{
		ArtifactID:  in.ArtifactID,
		TaskID:      t.TaskID,
		ContextID:   t.ContextID,
		Name:        in.Name,
		Description: in.Description,
		Parts:       in.Parts,
		Metadata:    in.Metadata,
	}
	saved, err := s.repo.AppendArtifact(ctx, a)
	if err != nil {
		return nil, err
	}

	// 刷新 task 活跃时间（updated_at），防止活跃 task 被 TTL 超时
	_ = s.repo.TouchActivity(ctx, t.TaskID)

	// 入发起方 inbox（被叫 agent 产出 artifact 给发起方）
	s.enqueueArtifact(ctx, t.FromAgentID, t.ToAgentID, saved)
	return saved, nil
}

// ─── 写入：状态转换 ─────────────────────────────────────────────────────

// TransitionInput 是 Transition 的入参。
type TransitionInput struct {
	TaskID        string
	CallerAgent   string
	CallerUID     int64
	ToState       State
	StatusMessage string
	ErrorMsg      string
}

// Transition 推动状态机。
//
// 谁能转到什么状态：
//   - canceled：发起方 或 被叫方都可以
//   - submitted / working / input-required / auth-required / completed / failed / rejected:
//     只有被叫方能触发（它们反映"被叫方的执行状态"）
//
// Gateway 只验合法性，不"主动替任何一方"转状态。
func (s *Service) Transition(ctx context.Context, in TransitionInput) (*Task, error) {
	if err := ValidateTaskID(in.TaskID); err != nil {
		return nil, err
	}

	t, _, _, err := s.repo.GetTask(ctx, in.TaskID, false, false)
	if err != nil {
		return nil, err
	}

	// participant 校验
	if in.CallerAgent != t.FromAgentID && in.CallerAgent != t.ToAgentID {
		return nil, ErrNotParticipant
	}
	owner, _, ok := s.agents.Lookup(ctx, in.CallerAgent)
	if !ok || owner != in.CallerUID {
		return nil, ErrNotParticipant
	}

	// 谁能转到哪个状态
	if err := checkTransitionCaller(in.ToState, in.CallerAgent, t); err != nil {
		return nil, err
	}

	// 合法性矩阵
	fromStates := StatesAllowingTransitionTo(in.ToState)
	if len(fromStates) == 0 {
		return nil, ErrInvalidTransition
	}

	changed, updated, err := s.repo.TransitionStatus(ctx, t.TaskID, fromStates, in.ToState, in.StatusMessage, in.ErrorMsg)
	if err != nil {
		return nil, err
	}
	if !changed {
		// CAS 失败：当前 status 不在 fromStates 里。并发情况下最新的 updated 已返回。
		return updated, ErrInvalidTransition
	}

	// 入对方 inbox
	peer := t.peerOf(in.CallerAgent)
	s.enqueueTransition(ctx, peer, t.TaskID, t.Status, in.ToState, in.StatusMessage)
	return updated, nil
}

// checkTransitionCaller 是谁能转到什么状态的规则。
func checkTransitionCaller(to State, callerAgent string, t *Task) error {
	switch to {
	case StateCanceled:
		// 双方都能 cancel
		return nil
	case StateWorking, StateInputRequired, StateAuthRequired,
		StateCompleted, StateFailed, StateRejected:
		// 只能被叫方（服务提供方）推这些状态
		if callerAgent != t.ToAgentID {
			return fmt.Errorf("task: only serving agent can transition to %s", to)
		}
		return nil
	case StateSubmitted:
		// 从 input-required / auth-required 回到 submitted —— 发起方补充完信息后让被叫方继续
		if callerAgent != t.FromAgentID {
			return fmt.Errorf("task: only initiating agent can transition back to submitted")
		}
		return nil
	}
	return ErrInvalidTransition
}

// ─── 读取 ──────────────────────────────────────────────────────────────

// Get 读取一个 Task 及其关联。调用方必须是 participant。
func (s *Service) Get(ctx context.Context, callerAgent string, callerUID int64, taskID string, withHistory, withArtifacts bool) (*Task, []*Message, []*Artifact, error) {
	t, history, arts, err := s.repo.GetTask(ctx, taskID, withHistory, withArtifacts)
	if err != nil {
		return nil, nil, nil, err
	}
	if callerAgent != t.FromAgentID && callerAgent != t.ToAgentID {
		return nil, nil, nil, ErrNotParticipant
	}
	owner, _, ok := s.agents.Lookup(ctx, callerAgent)
	if !ok || owner != callerUID {
		return nil, nil, nil, ErrNotParticipant
	}
	return t, history, arts, nil
}

// GetForUser 给 admin handler 用：caller 是用户（uid），不一定是 task 的具体参与方。
// 服务层先取 task，看 from/to 哪一边的 owner = uid，那边就作为 caller agent。
// 任意一边匹配都算授权通过——因为同 user 的所有 agent 都属于同一个人，用户自己看
// 自己 agent 之间的对话历史是合理的。
func (s *Service) GetForUser(ctx context.Context, uid int64, taskID string, withHistory, withArtifacts bool) (*Task, []*Message, []*Artifact, error) {
	t, history, arts, err := s.repo.GetTask(ctx, taskID, withHistory, withArtifacts)
	if err != nil {
		return nil, nil, nil, err
	}
	// 检查 from / to 任意一端属于 caller uid
	if fromOwner, _, ok := s.agents.Lookup(ctx, t.FromAgentID); ok && fromOwner == uid {
		return t, history, arts, nil
	}
	if toOwner, _, ok := s.agents.Lookup(ctx, t.ToAgentID); ok && toOwner == uid {
		return t, history, arts, nil
	}
	return nil, nil, nil, ErrNotParticipant
}

// ListByContext 返回同 contextID 下**调用方参与的** Task（从 list 中过滤）。
// 不做跨用户泄露。
func (s *Service) ListByContext(ctx context.Context, callerAgent string, callerUID int64, contextID string) ([]*Task, error) {
	if err := ValidateContextID(contextID); err != nil {
		return nil, err
	}
	all, err := s.repo.ListByContext(ctx, contextID)
	if err != nil {
		return nil, err
	}
	owner, _, ok := s.agents.Lookup(ctx, callerAgent)
	if !ok || owner != callerUID {
		return nil, ErrNotParticipant
	}
	out := make([]*Task, 0, len(all))
	for _, t := range all {
		if t.FromAgentID == callerAgent || t.ToAgentID == callerAgent {
			out = append(out, t)
		}
	}
	return out, nil
}

// ListTimeline 返回 context 下所有元数据事件，群组可见。
// 鉴权：caller 必须是 context 下某个 task 的参与者，或该 context 关联的群组成员。
// 为了简化，这里先只做"是参与者或同群"；具体 group 校验由上层在调之前做。
func (s *Service) ListTimeline(ctx context.Context, contextID string, sinceID int64, limit int) ([]*TimelineEntry, error) {
	if err := ValidateContextID(contextID); err != nil {
		return nil, err
	}
	return s.repo.ListTimeline(ctx, contextID, sinceID, limit)
}

// ListRecentByAgents 列出 agentIDs 任一方参与的近期 task，按 updated_at 倒序。
// Dashboard "Continue Working" 用，limit 默认 10，最大 50。
func (s *Service) ListRecentByAgents(ctx context.Context, agentIDs []string, limit int) ([]*Task, error) {
	return s.repo.ListRecentByAgents(ctx, agentIDs, limit)
}

// ─── 内部 helpers ─────────────────────────────────────────────────────

// resolveRole 根据 caller 在 Task 中的身份决定新 message 的 role。
func (s *Service) resolveRole(ctx context.Context, t *Task, callerAgent string, callerUID int64) (Role, error) {
	if callerAgent != t.FromAgentID && callerAgent != t.ToAgentID {
		return "", ErrNotParticipant
	}
	owner, _, ok := s.agents.Lookup(ctx, callerAgent)
	if !ok || owner != callerUID {
		return "", ErrNotParticipant
	}
	if callerAgent == t.FromAgentID {
		return RoleUser, nil
	}
	return RoleAgent, nil
}

// enqueueMessage / enqueueArtifact / enqueueTransition：inbox 入队。
// 失败不返回错误，只是"送达降级"—— 消息已在 DB，对方拉 task 详情可补。
//
// fromAgent 是消息/产物的真实发起者 agent_id（调用方从 task.FromAgentID 或
// task.ToAgentID 根据 role 传入），用于 timeline fan-out 标识来源。
func (s *Service) enqueueMessage(ctx context.Context, toAgent, fromAgent string, m *Message) {
	if s.outbox != nil {
		payload, _ := json.Marshal(m)
		eventType := "inbox.message:" + toAgent
		_ = s.outbox.Insert(ctx, eventType, payload)
		// 乐观直发 Kafka：成功则标记 sent，失败由 Dispatcher 兜底
		if s.kafka != nil {
			if err := s.kafka.Publish(ctx, "inbox.events", toAgent, payload); err == nil {
				_ = s.outbox.MarkSentByEventType(ctx, eventType, payload)
			}
		}
	} else if s.inbox != nil {
		// 旧路径：直接写 inbox 表
		s.retryEnqueue(ctx, func() error {
			return s.inbox.EnqueueMessage(ctx, toAgent, m)
		}, "message", m.TaskID)
	}
	s.fanOutTimeline(ctx, m.ContextID, TimelineUpdateInput{
		ContextID: m.ContextID,
		EntryKind: "message",
		TaskID:    m.TaskID,
		RefID:     m.MessageID,
		From:      fromAgent,
		Preview:   m.Preview,
	}, toAgent, fromAgent)
}

func (s *Service) enqueueArtifact(ctx context.Context, toAgent, fromAgent string, a *Artifact) {
	if s.inbox == nil {
		return
	}
	s.retryEnqueue(ctx, func() error {
		return s.inbox.EnqueueArtifact(ctx, toAgent, a)
	}, "artifact", a.TaskID)
	s.fanOutTimeline(ctx, a.ContextID, TimelineUpdateInput{
		ContextID: a.ContextID,
		EntryKind: "artifact",
		TaskID:    a.TaskID,
		RefID:     a.ArtifactID,
		Name:      a.Name,
		From:      fromAgent,
	}, toAgent, fromAgent)
}

func (s *Service) enqueueTransition(ctx context.Context, toAgent, taskID string, from, to State, statusMessage string) {
	if s.outbox != nil {
		payload, _ := json.Marshal(map[string]any{
			"task_id": taskID, "from": from, "to": to, "status_message": statusMessage,
		})
		eventType := "inbox.transition:" + toAgent
		_ = s.outbox.Insert(ctx, eventType, payload)
		// 乐观直发
		if s.kafka != nil {
			if err := s.kafka.Publish(ctx, "inbox.events", toAgent, payload); err == nil {
				_ = s.outbox.MarkSentByEventType(ctx, eventType, payload)
			}
		}
	} else if s.inbox != nil {
		s.retryEnqueue(ctx, func() error {
			return s.inbox.EnqueueTransition(ctx, toAgent, taskID, from, to, statusMessage)
		}, "transition", taskID)
	}
}

// retryEnqueue 对 inbox 入队操作做 3 次指数退避重试。
// inbox 入队失败是 CRITICAL——接收方将无法感知事件，只能靠 pull 兜底。
// 重试能覆盖瞬时网络抖动 / DB 连接池耗尽等场景。
func (s *Service) retryEnqueue(ctx context.Context, fn func() error, kind, taskID string) {
	for attempt := 0; attempt < 3; attempt++ {
		if fn() == nil {
			return
		}
		if attempt < 2 {
			time.Sleep(time.Duration(1<<attempt) * 100 * time.Millisecond) // 100ms, 200ms
		}
	}
	// 3 次都失败：靠 agent pull 兜底恢复
}

// fanOutTimeline 把 timeline_update 推给群组内的"旁观者"
// （即同群但非 direct participants 的其他成员）。
// groups 未装配或 context 不关联群组时，静默跳过。
func (s *Service) fanOutTimeline(ctx context.Context, contextID string, upd TimelineUpdateInput, directRecipients ...string) {
	if s.groups == nil {
		return
	}
	for _, recipient := range directRecipients {
		if recipient == "" {
			continue
		}
		peers, err := s.groups.MembersOfGroupsContaining(ctx, recipient)
		if err != nil || len(peers) == 0 {
			return
		}
		skip := map[string]bool{}
		for _, dr := range directRecipients {
			skip[dr] = true
		}
		for _, p := range peers {
			if skip[p] {
				continue
			}
			_ = s.inbox.EnqueueTimelineUpdate(ctx, p, upd)
			skip[p] = true // 同一 peer 只推一次
		}
		return
	}
}

// CleanupTerminalTasks 删除用户名下 before 之前的终态 task + 关联 messages/artifacts/inbox。
func (s *Service) CleanupTerminalTasks(ctx context.Context, ownerUID int64, before time.Time) (int, error) {
	return s.repo.DeleteTerminalTasksBefore(ctx, ownerUID, before)
}

// DeleteTask 删除单个 task（用户自选删除）。校验 task 的 from/to 任一端属于 caller uid。
func (s *Service) DeleteTask(ctx context.Context, callerUID int64, taskID string) error {
	t, _, _, err := s.repo.GetTask(ctx, taskID, false, false)
	if err != nil {
		return err
	}
	// 校验权限：from 或 to 的 owner 必须是 caller
	fromOwner, _, fromOK := s.agents.Lookup(ctx, t.FromAgentID)
	toOwner, _, toOK := s.agents.Lookup(ctx, t.ToAgentID)
	if (!fromOK || fromOwner != callerUID) && (!toOK || toOwner != callerUID) {
		return ErrNotParticipant
	}
	return s.repo.DeleteTaskByID(ctx, taskID)
}

// peerOf 是 Task 的一个 helper：给定一端的 agent_id 返回另一端。
// 不属于两端时返回空串。
func (t *Task) peerOf(agentID string) string {
	switch agentID {
	case t.FromAgentID:
		return t.ToAgentID
	case t.ToAgentID:
		return t.FromAgentID
	}
	return ""
}

// 让未用的 errors import 保留：Wrapping 等扩展会用到。
var _ = errors.New

// ─── Auto-close：闲聊连击兜底 ─────────────────────────────────────────

// AutoCloseChatterTasks 扫描 chat_streak >= minStreak 且 last_substantive_at 超过 cooldown 的 task，
// 强制 transition 到 completed 并通知双方。
// 由 cmd/messaging-svc 的定时 goroutine 每 30s 调一次。
func (s *Service) AutoCloseChatterTasks(ctx context.Context, minStreak int, cooldown time.Duration) (int, error) {
	before := time.Now().Add(-cooldown)
	tasks, err := s.repo.ListChatterTasks(ctx, minStreak, before)
	if err != nil {
		return 0, err
	}
	closed := 0
	for _, t := range tasks {
		ok, updated, err := s.repo.TransitionStatus(ctx, t.TaskID,
			[]State{StateSubmitted, StateWorking, StateInputRequired},
			StateCompleted,
			"auto-closed: idle chatter detected", "")
		if err != nil || !ok {
			continue
		}
		// 通知双方
		_ = s.inbox.EnqueueTransition(ctx, t.FromAgentID, t.TaskID, t.Status, StateCompleted, "auto-closed: idle chatter detected")
		_ = s.inbox.EnqueueTransition(ctx, t.ToAgentID, t.TaskID, t.Status, StateCompleted, "auto-closed: idle chatter detected")
		_ = updated // suppress unused
		closed++
	}
	return closed, nil
}

// TimeoutInactiveTasks 扫描 updated_at 超过 ttl 的非终态 task，标记为 failed。
// "活跃超时"：只要 task 有新消息/artifact/状态变更，updated_at 就会刷新。
// 超过 ttl 无任何活动才判定超时——不会误杀正在执行的长任务。
func (s *Service) TimeoutInactiveTasks(ctx context.Context, ttl time.Duration) (int, error) {
	cutoff := time.Now().Add(-ttl)
	tasks, err := s.repo.ListInactiveNonTerminal(ctx, cutoff)
	if err != nil {
		return 0, err
	}
	closed := 0
	for _, t := range tasks {
		ok, _, err := s.repo.TransitionStatus(ctx, t.TaskID,
			[]State{StateSubmitted, StateWorking, StateInputRequired},
			StateFailed,
			"timed out: no activity for "+ttl.String(), "")
		if err != nil || !ok {
			continue
		}
		if s.inbox != nil {
			_ = s.inbox.EnqueueTransition(ctx, t.FromAgentID, t.TaskID, t.Status, StateFailed, "timed out")
			_ = s.inbox.EnqueueTransition(ctx, t.ToAgentID, t.TaskID, t.Status, StateFailed, "timed out")
		}
		closed++
	}
	return closed, nil
}
