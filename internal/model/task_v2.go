package model

import (
	"time"

	"gorm.io/datatypes"
)

// TaskV2Status task 生命周期状态
type TaskV2Status string

const (
	TaskV2StatusActive  TaskV2Status = "active"  // 进行中
	TaskV2StatusClosed  TaskV2Status = "closed"  // 已关闭(任一成员主动关闭)
	TaskV2StatusTimeout TaskV2Status = "timeout" // 超过 expire_at 自动关闭
	TaskV2StatusFailed  TaskV2Status = "failed"  // 异常终止
)

// TaskMemberRole task 成员角色
type TaskMemberRole string

const (
	TaskMemberRoleCreator TaskMemberRole = "creator" // 发起方
	TaskMemberRoleMember  TaskMemberRole = "member"  // 普通成员
)

// TaskV2 一次多轮对话会话,参与方通过 task_messages 流往来沟通
// 命名 V2 是为了与已有的 async_task.go 中 AsyncTask (push 模式异步调用记录) 区分
type TaskV2 struct {
	TaskID          string       `gorm:"column:task_id;type:varchar(64);primaryKey"                         json:"task_id"`
	Title           string       `gorm:"column:title;type:varchar(255)"                                     json:"title"`
	CreatorAgentID  string       `gorm:"column:creator_agent_id;type:varchar(128);not null;index"           json:"creator_agent_id"`
	Status          TaskV2Status `gorm:"column:status;type:varchar(16);not null;index"                      json:"status"`
	ExpireAt        *time.Time   `gorm:"column:expire_at;index"                                             json:"expire_at,omitempty"`
	CreatedAt       time.Time    `gorm:"column:created_at;autoCreateTime"                                   json:"created_at"`
	UpdatedAt       time.Time    `gorm:"column:updated_at;autoUpdateTime"                                   json:"updated_at"`
	ClosedAt        *time.Time   `gorm:"column:closed_at"                                                   json:"closed_at,omitempty"`
}

func (TaskV2) TableName() string { return "tasks_v2" }

// TaskMember task 的参与方
type TaskMember struct {
	TaskID      string         `gorm:"column:task_id;type:varchar(64);primaryKey"               json:"task_id"`
	AgentID     string         `gorm:"column:agent_id;type:varchar(128);primaryKey"             json:"agent_id"`
	Role        TaskMemberRole `gorm:"column:role;type:varchar(16);not null"                    json:"role"`
	JoinedAt    time.Time      `gorm:"column:joined_at;autoCreateTime"                          json:"joined_at"`
	LeftAt      *time.Time     `gorm:"column:left_at"                                           json:"left_at,omitempty"`
	LastReadSeq int            `gorm:"column:last_read_seq;not null;default:0"                  json:"last_read_seq"`
}

func (TaskMember) TableName() string { return "task_members" }

// TaskMessage task 内的一条消息
// content 字段存 A2A Message 的 parts 数组,JSON 格式:
//   [{"kind":"text","text":"..."},{"kind":"data","data":{...}}]
type TaskMessage struct {
	TaskID        string         `gorm:"column:task_id;type:varchar(64);primaryKey"                    json:"task_id"`
	Seq           int            `gorm:"column:seq;primaryKey;autoIncrement:false"                     json:"seq"`
	SenderAgentID string         `gorm:"column:sender_agent_id;type:varchar(128);not null;index"       json:"sender_agent_id"`
	MessageID     string         `gorm:"column:message_id;type:varchar(64);not null;uniqueIndex"       json:"message_id"`
	Content       datatypes.JSON `gorm:"column:content;type:json;not null"                             json:"content"`
	CreatedAt     time.Time      `gorm:"column:created_at;autoCreateTime;index"                        json:"created_at"`
}

func (TaskMessage) TableName() string { return "task_messages" }
