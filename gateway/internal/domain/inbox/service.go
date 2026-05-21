package inbox

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Ggrryta/agent-mesh/gateway/internal/domain/task"
)

const (
	pollInterval    = 500 * time.Millisecond
	maxWaitTimeout  = 30 * time.Second
	defaultPullLimit = 100
)

// Service 是 inbox 的业务入口：为其它 domain 提供 Enqueue* 方法
// （同时实现 task.Inboxer 接口），为 API 层提供 Pull 方法。
//
// notifier 是可选的下游钩子，每次 Enqueue 成功后被调用；典型下游是
// delivery.Pusher，让它尝试 push 这条事件。notifier 为 nil 时不通知。
// notifier 应当是**非阻塞**的（比如往 channel 投递，满了丢弃），避免拖慢
// API 请求。
type Service struct {
	repo     Repo
	notifier func(*Event)
	// kafka 是可选的 Kafka producer。非 nil 时 Enqueue 后异步双写到 Kafka。
	// Phase 1：Kafka 是副本，inbox 表仍是主路径。
	kafka KafkaPublisher
}

// KafkaPublisher 是 Kafka 发布接口，解耦具体实现。
type KafkaPublisher interface {
	Publish(ctx context.Context, topic, key string, value []byte) error
}

// NewService 装配。notifier 可传 nil。
func NewService(repo Repo) *Service { return &Service{repo: repo} }

// WithNotifier 注入下游通知钩子（如 delivery.Pusher.NotifyEvent）。
// 返回自身以支持链式调用。
func (s *Service) WithNotifier(fn func(*Event)) *Service {
	s.notifier = fn
	return s
}

// WithKafka 注入 Kafka producer（Phase 1 双写模式）。
func (s *Service) WithKafka(k KafkaPublisher) *Service {
	s.kafka = k
	return s
}

// ─── 入队（被 task.Service 调用）─────────────────────────────────

// EnqueueMessage 把一条 message 事件写到 toAgentID 的 inbox。
//
// payload 是完整的 message JSON（含 parts 和 metadata），让 agent 拉 inbox
// 时一次拿全，不用再去 task_messages 表 JOIN。
func (s *Service) EnqueueMessage(ctx context.Context, toAgentID string, m *task.Message) error {
	if toAgentID == "" || m == nil {
		return fmt.Errorf("inbox: EnqueueMessage: missing target or message")
	}
	payload, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("inbox: marshal message: %w", err)
	}
	saved, err := s.repo.Insert(ctx, &Event{
		AgentID: toAgentID,
		Kind:    KindMessage,
		TaskID:  m.TaskID,
		RefID:   m.MessageID,
		Payload: payload,
	})
	if err != nil {
		return err
	}
	s.notify(saved)
	return nil
}

// EnqueueArtifact 入一条 artifact 事件。
func (s *Service) EnqueueArtifact(ctx context.Context, toAgentID string, a *task.Artifact) error {
	if toAgentID == "" || a == nil {
		return fmt.Errorf("inbox: EnqueueArtifact: missing target or artifact")
	}
	payload, err := json.Marshal(a)
	if err != nil {
		return fmt.Errorf("inbox: marshal artifact: %w", err)
	}
	saved, err := s.repo.Insert(ctx, &Event{
		AgentID: toAgentID,
		Kind:    KindArtifact,
		TaskID:  a.TaskID,
		RefID:   a.ArtifactID,
		Payload: payload,
	})
	if err != nil {
		return err
	}
	s.notify(saved)
	return nil
}

// EnqueueTransition 入一条状态变更事件。
func (s *Service) EnqueueTransition(ctx context.Context, toAgentID, taskID string, from, to task.State, statusMessage string) error {
	if toAgentID == "" || taskID == "" {
		return fmt.Errorf("inbox: EnqueueTransition: missing target or task_id")
	}
	payload, err := json.Marshal(TransitionPayload{
		TaskID:        taskID,
		FromState:     from,
		ToState:       to,
		StatusMessage: statusMessage,
	})
	if err != nil {
		return fmt.Errorf("inbox: marshal transition: %w", err)
	}
	saved, err := s.repo.Insert(ctx, &Event{
		AgentID: toAgentID,
		Kind:    KindTransition,
		TaskID:  taskID,
		RefID:   string(to),
		Payload: payload,
	})
	if err != nil {
		return err
	}
	s.notify(saved)
	return nil
}

// notify 把新 event 传给下游 notifier（如 push worker），nil 时忽略。
func (s *Service) notify(e *Event) {
	if s.notifier != nil && e != nil {
		s.notifier(e)
	}
	// Phase 1 双写：异步发 Kafka（不阻塞主流程，失败只丢副本）
	if s.kafka != nil && e != nil {
		go func() {
			_ = s.kafka.Publish(context.Background(), "inbox.events", e.AgentID, e.Payload)
		}()
	}
}

// 编译期断言：Service 实现 task.Inboxer。
var _ task.Inboxer = (*Service)(nil)

// ─── 拉取（被 API 层调用）─────────────────────────────────────────

// Pull 返回 agentID 的事件，id > sinceID，按 id 升序，limit 截断。
// 返回 (events, maxID, error)：maxID 作为下一次 cursor。
func (s *Service) Pull(ctx context.Context, agentID string, sinceID int64, limit int) ([]*Event, int64, error) {
	events, err := s.repo.ListSince(ctx, agentID, sinceID, limit)
	if err != nil {
		return nil, 0, err
	}
	maxID := sinceID
	for _, e := range events {
		if e.ID > maxID {
			maxID = e.ID
		}
	}
	return events, maxID, nil
}

// PollWithWait 是 Pull 的 long-poll 版本。先查一次，有数据立即返回；
// 无数据则每 500ms 重查直到有结果或 wait 超时。wait=0 等价于 Pull。
func (s *Service) PollWithWait(ctx context.Context, agentID string, sinceID int64, limit int, wait time.Duration) ([]*Event, int64, error) {
	if wait <= 0 {
		return s.Pull(ctx, agentID, sinceID, limit)
	}
	if wait > maxWaitTimeout {
		wait = maxWaitTimeout
	}

	events, maxID, err := s.Pull(ctx, agentID, sinceID, limit)
	if err != nil || len(events) > 0 {
		return events, maxID, err
	}

	deadline := time.After(wait)
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil, sinceID, ctx.Err()
		case <-deadline:
			return nil, sinceID, nil
		case <-ticker.C:
			events, maxID, err = s.Pull(ctx, agentID, sinceID, limit)
			if err != nil || len(events) > 0 {
				return events, maxID, err
			}
		}
	}
}

// MarkDelivered 供 push worker 成功推送后回写。
func (s *Service) MarkDelivered(ctx context.Context, ids []int64) error {
	return s.repo.MarkDelivered(ctx, ids)
}

// TimelineUpdatePayload 是 KindTimelineUpdate 的 payload 结构。
// 只含元数据，agent 看到后可以主动拉 timeline 或单条详情。
type TimelineUpdatePayload struct {
	ContextID string `json:"context_id"`
	EntryKind string `json:"entry_kind"` // message | artifact | transition
	TaskID    string `json:"task_id"`
	RefID     string `json:"ref_id"`
	From      string `json:"from"`
	Preview   string `json:"preview,omitempty"`
	Name      string `json:"name,omitempty"`
}

// EnqueueTimelineUpdate 给某个旁观者（非 from/to）的 inbox 推一条 timeline 更新通知。
// agent 收到后可决定是否拉详情。签名对齐 task.TimelineUpdateInput 的字段。
func (s *Service) EnqueueTimelineUpdate(ctx context.Context, toAgentID string, p task.TimelineUpdateInput) error {
	if toAgentID == "" {
		return fmt.Errorf("inbox: EnqueueTimelineUpdate: missing target")
	}
	payload := TimelineUpdatePayload{
		ContextID: p.ContextID,
		EntryKind: p.EntryKind,
		TaskID:    p.TaskID,
		RefID:     p.RefID,
		From:      p.From,
		Preview:   p.Preview,
		Name:      p.Name,
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("inbox: marshal timeline update: %w", err)
	}
	saved, err := s.repo.Insert(ctx, &Event{
		AgentID: toAgentID,
		Kind:    KindTimelineUpdate,
		TaskID:  p.TaskID,
		RefID:   p.RefID,
		Payload: data,
	})
	if err != nil {
		return err
	}
	s.notify(saved)
	return nil
}

// NotificationPayload 是 Kind=notification 时的 payload 结构。
type NotificationPayload struct {
	FromAgentID string `json:"from_agent_id"`
	GroupID     string `json:"group_id"`
	Text        string `json:"text"`
}

// EnqueueNotification 向指定 agent 投递一条群组通知。通知是单向的，不期望回复。
func (s *Service) EnqueueNotification(ctx context.Context, toAgentID, fromAgentID, groupID, text string) error {
	if toAgentID == "" {
		return fmt.Errorf("inbox: EnqueueNotification: missing target")
	}
	payload := NotificationPayload{
		FromAgentID: fromAgentID,
		GroupID:     groupID,
		Text:        text,
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("inbox: marshal notification: %w", err)
	}
	refID := fmt.Sprintf("notif-%s-%d", fromAgentID, time.Now().UnixMilli())
	saved, err := s.repo.Insert(ctx, &Event{
		AgentID: toAgentID,
		Kind:    KindNotification,
		TaskID:  "", // 通知不关联 task
		RefID:   refID,
		Payload: data,
	})
	if err != nil {
		return err
	}
	s.notify(saved)
	return nil
}
