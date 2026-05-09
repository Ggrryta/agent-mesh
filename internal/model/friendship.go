package model

import "time"

// FriendshipStatus Agent 好友关系状态
type FriendshipStatus string

const (
	FriendshipStatusPending  FriendshipStatus = "pending"
	FriendshipStatusAccepted FriendshipStatus = "accepted"
	FriendshipStatusRejected FriendshipStatus = "rejected"
	FriendshipStatusRevoked  FriendshipStatus = "revoked"
)

// Friendship Agent 间对称好友关系
// AgentAID / AgentBID 按字典序规范化,保证 (A,B) 和 (B,A) 只存一行
// 查询某 agent 的好友时,需要 WHERE agent_a_id=? OR agent_b_id=?
type Friendship struct {
	ID          int64            `gorm:"primaryKey;autoIncrement"                                         json:"id"`
	AgentAID    string           `gorm:"column:agent_a_id;type:varchar(128);not null;uniqueIndex:uk_pair" json:"agent_a_id"`
	AgentBID    string           `gorm:"column:agent_b_id;type:varchar(128);not null;uniqueIndex:uk_pair" json:"agent_b_id"`
	Status      FriendshipStatus `gorm:"column:status;type:varchar(16);not null;index:idx_a_status,priority:2;index:idx_b_status,priority:2" json:"status"`
	InitiatorID string           `gorm:"column:initiator_id;type:varchar(128);not null;index"             json:"initiator_id"`
	Reason      string           `gorm:"column:reason;type:text"                                          json:"reason,omitempty"`
	AcceptedAt  *time.Time       `gorm:"column:accepted_at"                                               json:"accepted_at,omitempty"`
	CreatedAt   time.Time        `gorm:"column:created_at;autoCreateTime"                                 json:"created_at"`
	UpdatedAt   time.Time        `gorm:"column:updated_at;autoUpdateTime"                                 json:"updated_at"`
}

func (Friendship) TableName() string { return "friendships" }

// NormalizePair 按字典序规范化一对 agent_id,保证 (A,B) 和 (B,A) 都落到同一行
func NormalizePair(a, b string) (string, string) {
	if a < b {
		return a, b
	}
	return b, a
}

// Counterpart 在一个 friendship 中,给定其中一方的 agent_id,返回对方
func (f *Friendship) Counterpart(self string) string {
	if f.AgentAID == self {
		return f.AgentBID
	}
	return f.AgentAID
}
