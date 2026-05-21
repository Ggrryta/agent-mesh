// Package task 负责 A2A Task 的持久化与状态机校验。
//
// 定位（详见 ADR 002）：
//   - Gateway 是消息中枢，**不执行任务**，不调 agent，不做 orchestration
//   - Task 的状态推进由两端 agent 各自汇报；Gateway 只做"转换是否合法"的校验
//   - 三张表承载 A2A 协议：
//   - reliable_async_tasks     一次有状态工作单元（Task 主表）
//   - task_messages            Task.history[]，每一轮对话
//   - task_artifacts           Task.artifacts[]，被叫 agent 产出的交付物
//
// 本包不含：
//   - Worker / 执行 / 重试 / 孤儿回滚（Gateway 不占任务）
//   - push 送达逻辑（在 internal/delivery）
//   - inbox 入队（在 domain/inbox）—— task 包的 service 调 inbox.Enqueue，
//     但不依赖 inbox 的内部结构
package task

import (
	"errors"
	"regexp"
	"strings"
	"time"
)

// State 对齐 A2A TaskState 枚举。九个状态中前四个是"流转态"，后五个是"终态"。
// https://github.com/a2aproject/A2A/blob/main/docs/topics/life-of-a-task.md
type State string

const (
	StateSubmitted     State = "submitted"      // 已提交，等被叫 agent 开始处理
	StateWorking       State = "working"        // 被叫 agent 正在处理
	StateInputRequired State = "input-required" // 需要发起方补充信息
	StateAuthRequired  State = "auth-required"  // 需要额外授权（MVP 不生产这个状态，保留对齐）

	// 终态（一旦进入不可再转出）
	StateCompleted State = "completed"
	StateCanceled  State = "canceled"
	StateFailed    State = "failed"
	StateRejected  State = "rejected"
)

// IsTerminal 报告该状态是否终态；终态的 Task 不接受任何新的 transition。
func (s State) IsTerminal() bool {
	switch s {
	case StateCompleted, StateCanceled, StateFailed, StateRejected:
		return true
	}
	return false
}

// Role 区分一条 message 是发起方（user）还是被叫方（agent）发出。
// 对齐 A2A Message.role。
type Role string

const (
	RoleUser  Role = "user"  // 发起方（Alice 视角）
	RoleAgent Role = "agent" // 被叫方（Bob 视角）
)

// PartKind 枚举 Part 的四种承载形式，对齐 A2A Part 的 oneof。
//
// MVP 阶段 Gateway 不解析 Part 内容，仅存 JSON 原样转发；PartKind 只是给
// agent 和 UI 一个 hint。
type PartKind string

const (
	PartText PartKind = "text" // 纯文本
	PartRaw  PartKind = "raw"  // 内联二进制（base64）
	PartURL  PartKind = "url"  // 外部引用（S3、HTTP 等）
	PartData PartKind = "data" // 结构化 JSON
)

// Part 是 Message.parts[] / Artifact.parts[] 的元素。
//
// 四种 Kind 互斥：Kind=text 时 Text 有效，其它字段都应为空。MVP 阶段
// 校验较宽松（按需读字段），但 Kind 必填。
type Part struct {
	Kind      PartKind       `json:"kind"`
	Text      string         `json:"text,omitempty"`
	Raw       []byte         `json:"raw,omitempty"` // base64 编解码由 json 标准库负责
	URL       string         `json:"url,omitempty"`
	Data      map[string]any `json:"data,omitempty"` // 任意 JSON 对象
	Filename  string         `json:"filename,omitempty"`
	MediaType string         `json:"mediaType,omitempty"`
	Metadata  map[string]any `json:"metadata,omitempty"`
}

// Message 对齐 A2A Message。在 Task 的 history[] 中按 created_at 顺序排列。
//
// ID 即 A2A 的 Message.messageId：**全局唯一**，UNIQUE 索引做幂等。
// 同一 Message 不允许修改，只能追加新 Message。
//
// JSON tag 对齐 A2A 协议字段名；内部自增 ID 不外暴。
type Message struct {
	ID         int64          `json:"-"`
	MessageID  string         `json:"message_id"`
	TaskID     string         `json:"task_id"`
	ContextID  string         `json:"context_id"`
	Role       Role           `json:"role"`
	Parts      []Part         `json:"parts"`
	Preview    string         `json:"preview,omitempty"`
	Metadata   map[string]any `json:"metadata,omitempty"`
	RefTaskIDs []string       `json:"reference_task_ids,omitempty"`
	CreatedAt  time.Time      `json:"created_at"`
}

// Artifact 对齐 A2A Artifact。由被叫 agent 产出，归属某个 Task。
//
// ArtifactID 在 Task 内唯一（UNIQUE (task_id, artifact_id)）。
// 同名 artifact 通常表示不同版本（比如 sailboat_v1 / v2），但 MVP 阶段
// 不做版本管理，两个同名 artifact 只是两行独立记录。
type Artifact struct {
	ID          int64          `json:"-"`
	ArtifactID  string         `json:"artifact_id"`
	TaskID      string         `json:"task_id"`
	ContextID   string         `json:"context_id"`
	Name        string         `json:"name,omitempty"`
	Description string         `json:"description,omitempty"`
	Parts       []Part         `json:"parts"`
	Metadata    map[string]any `json:"metadata,omitempty"`
	CreatedAt   time.Time      `json:"created_at"`
}

// Task 是主表 reliable_async_tasks 的 Go 映射。
//
// 注意 Gateway 不维护 Worker 相关字段（Retries / NextRunAt / ClaimedAt）
// 的业务语义 —— migration 0001 里这几列保留是为了**不改 schema**，future
// 演进时可能用上；MVP 阶段它们永远是 NULL / 0。
type Task struct {
	ID            int64          `json:"-"`
	TaskID        string         `json:"task_id"`
	ContextID     string         `json:"context_id"`
	FromAgentID   string         `json:"from_agent_id"`
	ToAgentID     string         `json:"to_agent_id"`
	Status        State          `json:"status"`
	StatusMessage string         `json:"status_message,omitempty"`
	ErrorMsg      string         `json:"error,omitempty"`
	Metadata      map[string]any `json:"metadata,omitempty"`
	CreatedAt     time.Time      `json:"created_at"`
	UpdatedAt     time.Time      `json:"updated_at"`
}

// 域错误。API handler 据此翻 HTTP code。
var (
	ErrTaskNotFound        = errors.New("task: not found")
	ErrMessageNotFound     = errors.New("task: message not found")
	ErrInvalidTaskID       = errors.New("task: invalid task_id format")
	ErrInvalidMessageID    = errors.New("task: invalid message_id format")
	ErrInvalidArtifactID   = errors.New("task: invalid artifact_id format")
	ErrInvalidContextID    = errors.New("task: invalid context_id format")
	ErrInvalidRole         = errors.New("task: invalid message role")
	ErrInvalidPartKind     = errors.New("task: invalid part kind")
	ErrMessageIDDuplicate  = errors.New("task: message_id already exists with different content")
	ErrArtifactIDDuplicate = errors.New("task: artifact_id already exists within task")
	ErrInvalidTransition   = errors.New("task: invalid state transition")
	ErrNotParticipant      = errors.New("task: caller is not participant of the task")
)

// 标识符格式约束。全部走 3~64 字符、URL 安全字符集。
// MessageID / ArtifactID 放宽到 128，给 UUID / 客户端加前缀留空间。
// 字符集包含 @：agent_id 用 @ 做命名空间分隔（"slug@username"），
// 客户端常把 agent_id 嵌到 task/message id 里。
var (
	taskIDRE     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:@-]{2,63}$`)
	contextIDRE  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:@-]{2,63}$`)
	messageIDRE  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:@-]{2,127}$`)
	artifactIDRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:@-]{2,127}$`)
)

// ValidateTaskID / ValidateContextID / ValidateMessageID / ValidateArtifactID
// 让 service 层在入 DB 前做格式校验。
func ValidateTaskID(s string) error {
	if !taskIDRE.MatchString(s) {
		return ErrInvalidTaskID
	}
	return nil
}

func ValidateContextID(s string) error {
	if !contextIDRE.MatchString(s) {
		return ErrInvalidContextID
	}
	return nil
}

func ValidateMessageID(s string) error {
	if !messageIDRE.MatchString(s) {
		return ErrInvalidMessageID
	}
	return nil
}

func ValidateArtifactID(s string) error {
	if !artifactIDRE.MatchString(s) {
		return ErrInvalidArtifactID
	}
	return nil
}

// ValidateRole 拒绝任何非 user / agent 的字符串。
func ValidateRole(r Role) error {
	switch r {
	case RoleUser, RoleAgent:
		return nil
	}
	return ErrInvalidRole
}

// ValidateParts 至少一个 Part；每个 Part 必须有合法 Kind。
// 刻意不校验 Kind 对应字段是否非空 —— MVP 存 JSON 透传，内容由 agent 解释。
func ValidateParts(parts []Part) error {
	if len(parts) == 0 {
		return errors.New("task: message/artifact must have at least one part")
	}
	for _, p := range parts {
		switch p.Kind {
		case PartText, PartRaw, PartURL, PartData:
			// OK
		default:
			return ErrInvalidPartKind
		}
	}
	return nil
}

// allowedTransitions 是状态机的合法转换表。
//
// 规则：
//   - submitted → working / canceled / rejected
//   - working → completed / failed / canceled / input-required / auth-required
//   - input-required → submitted / working / canceled   （发起方补完消息后 agent 可直接转 working 继续）
//   - auth-required → submitted / canceled               （补授权后回到 submitted 排队）
//   - 终态（completed / canceled / failed / rejected）：不允许再转出
//
// 关键边界：
//   - **canceled 是"退出逃生门"**，任何非终态都可以转 canceled（由发起方触发）
//   - submitted → completed 不允许，必须先经 working（强迫 agent 汇报开始）
//     这样"执行过程"始终可追溯
var allowedTransitions = map[State]map[State]bool{
	StateSubmitted: {
		StateWorking:  true,
		StateCanceled: true,
		StateRejected: true,
	},
	StateWorking: {
		StateCompleted:     true,
		StateFailed:        true,
		StateCanceled:      true,
		StateInputRequired: true,
		StateAuthRequired:  true,
	},
	StateInputRequired: {
		StateSubmitted: true,
		StateWorking:   true,
		StateCanceled:  true,
	},
	StateAuthRequired: {
		StateSubmitted: true,
		StateCanceled:  true,
	},
	// 终态没有出边
}

// IsAllowedTransition 判断 from → to 是否合法。
// 未知 from 状态一律视为非法（防御式；不应该发生）。
func IsAllowedTransition(from, to State) bool {
	nexts, ok := allowedTransitions[from]
	if !ok {
		return false
	}
	return nexts[to]
}

// StatesAllowingTransitionTo 返回能转到 to 的所有 from 集合。
// repo 层做 CAS UPDATE 时需要一个 `status IN (...)` 清单：
// 先查本表拿 "能转到 to 的所有 from"，再拼 SQL。
func StatesAllowingTransitionTo(to State) []State {
	out := make([]State, 0, 4)
	for from, nexts := range allowedTransitions {
		if nexts[to] {
			out = append(out, from)
		}
	}
	return out
}

// normalizeID 归一化标识符：trim + 保留大小写（和 agent_id 不同，
// task/context/message/artifact id 我们允许大小写区分，因为它们常来自
// UUID / 客户端自定义规则，强制小写会破坏）。
func normalizeID(s string) string {
	return strings.TrimSpace(s)
}

// TimelineEntry 是 context 时间轴里的一条元数据记录。
// 群组成员都能看到全量 timeline，但正文需要另外拉取。
type TimelineEntry struct {
	Kind       string    `json:"kind"`        // "message" | "artifact" | "transition"
	TaskID     string    `json:"task_id"`
	ContextID  string    `json:"context_id"`
	EntryID    int64     `json:"entry_id"`    // 内部 id，用作分页 cursor
	RefID      string    `json:"ref_id"`      // message_id / artifact_id / to_state
	From       string    `json:"from"`        // 发起 agent（对 message 是 sender）
	To         string    `json:"to,omitempty"`
	Preview    string    `json:"preview,omitempty"`    // 仅 message 有
	Name       string    `json:"name,omitempty"`       // 仅 artifact 有
	Descrption string    `json:"description,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
}
