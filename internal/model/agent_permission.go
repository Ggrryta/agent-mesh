package model

import "time"

// AgentPermission consumer 对 Agent 的调用授权记录
type AgentPermission struct {
	ID            int64     `gorm:"primaryKey;autoIncrement"                                                          json:"id"`
	AgentID       string    `gorm:"column:agent_id;type:varchar(128);uniqueIndex:uidx_agent_consumer;not null"        json:"agent_id"`
	OwnerAppID    string    `gorm:"column:owner_app_id;type:varchar(128);not null"                                    json:"owner_app_id"`
	ConsumerAppID string    `gorm:"column:consumer_app_id;type:varchar(128);uniqueIndex:uidx_agent_consumer;not null" json:"consumer_app_id"`
	GrantedAt     time.Time `gorm:"column:granted_at;not null"                                                        json:"granted_at"`
	CreatedAt     time.Time `gorm:"column:created_at;autoCreateTime"                                                  json:"created_at"`
}

func (AgentPermission) TableName() string { return "agent_permissions" }

// AgentApply consumer 向 Agent owner 提交的调用权限申请
type AgentApply struct {
	ID             int64       `gorm:"primaryKey;autoIncrement"                                          json:"id"`
	AgentID        string      `gorm:"column:agent_id;type:varchar(128);not null;index"                  json:"agent_id"`
	OwnerAppID     string      `gorm:"column:owner_app_id;type:varchar(128);not null;index"              json:"owner_app_id"`
	ApplicantAppID string      `gorm:"column:applicant_app_id;type:varchar(128);not null;index"          json:"applicant_app_id"`
	Reason         string      `gorm:"column:reason;type:varchar(512)"                                   json:"reason"`
	Status         ApplyStatus `gorm:"column:status;not null;default:1"                                  json:"status"`
	ReviewedAt     *time.Time  `gorm:"column:reviewed_at"                                                json:"reviewed_at"`
	CreatedAt      time.Time   `gorm:"column:created_at;autoCreateTime"                                  json:"created_at"`
	UpdatedAt      time.Time   `gorm:"column:updated_at;autoUpdateTime"                                  json:"updated_at"`
}

func (AgentApply) TableName() string { return "agent_applies" }
