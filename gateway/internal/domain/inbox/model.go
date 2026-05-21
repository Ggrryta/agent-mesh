// Package inbox 承载每 agent 的事件收件箱。
//
// 定位（详见 ADR 010）：
//   - inbox 是真相之源：所有发给某 agent 的事件（message / artifact / transition）
//     先写到这里，再尝试 push 送达
//   - agent 通过 GET /v1/mesh/inbox?since=X 增量拉取
//   - delivered_at 只打 push 成功的标，pull 不清它 —— 允许同一事件被 push
//     和 pull 都收到，agent 侧按 id 去重
//
// 不做的事（与 ADR 010 对齐）：
//   - 不重试 push：一次失败就留 inbox，等下次 task 动作或 agent 主动拉
//   - 不做 GC：历史事件保留，便于审计（MVP 不管）
package inbox

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/Ggrryta/agent-mesh/gateway/internal/domain/task"
)

// Kind 枚举事件种类。
type Kind string

const (
	KindMessage        Kind = "message"
	KindArtifact       Kind = "artifact"
	KindTransition     Kind = "transition"
	KindTimelineUpdate Kind = "timeline_update" // 群组 context 有新活动的元数据通知
	KindNotification   Kind = "notification"    // 群组通知（单向推送，不期望回复）
)

// Event 是落库的单条 inbox 行。payload 根据 Kind 不同：
//   - message：完整的 *task.Message 结构（MessageID / TaskID / Role / Parts / ...）
//   - artifact：完整的 *task.Artifact
//   - transition：TransitionPayload
type Event struct {
	ID          int64
	AgentID     string // 收件人
	Kind        Kind
	TaskID      string
	RefID       string // message_id / artifact_id / to_state
	Payload     json.RawMessage
	CreatedAt   time.Time
	DeliveredAt *time.Time
}

// TransitionPayload 是 Kind=transition 时的 payload 结构。
type TransitionPayload struct {
	TaskID        string     `json:"task_id"`
	FromState     task.State `json:"from"`
	ToState       task.State `json:"to"`
	StatusMessage string     `json:"status_message,omitempty"`
}

// 域错误。
var (
	ErrEmptyAgent = errors.New("inbox: agent_id required")
)

// Repo 是 inbox 的数据访问接口。
type Repo interface {
	Insert(ctx context.Context, e *Event) (*Event, error)
	// ListSince 返回 agent_id 的事件，id > sinceID，按 id 升序，limit 截断。
	ListSince(ctx context.Context, agentID string, sinceID int64, limit int) ([]*Event, error)
	// MarkDelivered 批量把一组 id 打上 delivered_at。push 成功时调用。
	MarkDelivered(ctx context.Context, ids []int64) error
}
