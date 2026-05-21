// Package friendship 管理 agent 之间的好友关系。
//
// 约束（与 ADR 008 对齐）：
//   - 粒度是 agent ↔ agent，不是 user ↔ user。
//   - 两端都必须是 kind='normal' 的 agent。virtual-user-* 不参与显式 friendship；
//     它和其 owner 名下所有 normal agent 默认互为好友（由 AreFriends 短路）。
//   - 发起 / 接受 / 拒绝 / 撤销 全部由 **agent 的 owner** 在 Admin API 里操作，
//     agent 自身（通过 mesh API）不触碰 friendship。
//   - 覆盖式重试：一对 (from, to) 只有一行；rejected/revoked 状态下可以用
//     Request 把它"激活"回 pending，覆盖 reason。已 accepted 不能被 Request
//     覆盖（要先 Revoke）。
//
// 该包只关心 friendship 自身语义；"能不能给 virtual-user 打任务"等更广义的
// 访问控制由其他 domain（Task）负责。
package friendship

import (
	"errors"
	"time"
)

// Status 是 friendship 的状态机。
type Status string

const (
	// StatusPending：发起方已请求，等待接收方响应。
	StatusPending Status = "pending"
	// StatusAccepted：双向生效，可互发消息。
	StatusAccepted Status = "accepted"
	// StatusRejected：接收方拒绝。仍保留行以支持"再请求"。
	StatusRejected Status = "rejected"
	// StatusRevoked：任一方主动撤销已建立的关系。保留行同上。
	StatusRevoked Status = "revoked"
)

// IsTerminal 报告某状态能否被 Request 覆盖回 pending。
// accepted 和 pending 都不能 —— 前者要先 Revoke，后者自身就是活跃请求。
func (s Status) IsTerminal() bool {
	return s == StatusRejected || s == StatusRevoked
}

// Friendship 对应 friendships 表一行。
type Friendship struct {
	ID          int64
	FromAgentID string
	ToAgentID   string
	Status      Status
	Reason      string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// Involves 判断某个 agent_id 是否是本关系两端之一。查询 / 权限判断用。
func (f *Friendship) Involves(agentID string) bool {
	return f.FromAgentID == agentID || f.ToAgentID == agentID
}

// Peer 返回"对方" agent_id。
// 调用方保证 agentID 确实是两端之一，否则返回空串。
func (f *Friendship) Peer(agentID string) string {
	switch agentID {
	case f.FromAgentID:
		return f.ToAgentID
	case f.ToAgentID:
		return f.FromAgentID
	}
	return ""
}

// 域错误。API handler 据此翻译 HTTP code。
var (
	ErrNotFound          = errors.New("friendship: not found")
	ErrSelfFriend        = errors.New("friendship: cannot friend self")
	ErrInvalidAgent      = errors.New("friendship: invalid agent id")
	ErrVirtualUserPeer   = errors.New("friendship: virtual-user agents cannot participate")
	ErrNotOwner          = errors.New("friendship: caller is not the agent owner")
	ErrAlreadyAccepted   = errors.New("friendship: already accepted")
	ErrAlreadyPending    = errors.New("friendship: already pending")
	ErrInvalidTransition = errors.New("friendship: invalid state transition")
)
