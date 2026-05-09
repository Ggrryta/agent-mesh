package model

import (
	"time"

	"gorm.io/datatypes"
)

const (
	ReliabilityRedis    = "redis"
	ReliabilityReliable = "reliable"

	AsyncTaskStatusRetrying AsyncTaskStatus = "retrying"
)

// ReliableAsyncTask is the durable task fact record used by reliable async mode.
type ReliableAsyncTask struct {
	ID        int64          `gorm:"primaryKey;autoIncrement" json:"id"`
	TaskID    string         `gorm:"column:task_id;type:varchar(64);uniqueIndex;not null" json:"task_id"`
	AgentID   string         `gorm:"column:agent_id;type:varchar(128);index;not null" json:"agent_id"`
	SkillID   string         `gorm:"column:skill_id;type:varchar(128)" json:"skill_id,omitempty"`
	AppID     string         `gorm:"column:app_id;type:varchar(128);index;not null" json:"app_id"`
	Input     datatypes.JSON `gorm:"column:input;type:json" json:"input"`
	Output    datatypes.JSON `gorm:"column:output;type:json" json:"output,omitempty"`
	Status    string         `gorm:"column:status;type:varchar(32);index;not null" json:"status"`
	ErrorMsg  string         `gorm:"column:error_msg;type:text" json:"error_msg,omitempty"`
	Retries   int            `gorm:"column:retries;not null;default:0" json:"retries"`
	NextRunAt *time.Time     `gorm:"column:next_run_at;index" json:"next_run_at,omitempty"`
	Version   int64          `gorm:"column:version;not null;default:0" json:"version"`
	CreatedAt time.Time      `gorm:"column:created_at;autoCreateTime" json:"created_at"`
	UpdatedAt time.Time      `gorm:"column:updated_at;autoUpdateTime" json:"updated_at"`
}

func (ReliableAsyncTask) TableName() string { return "reliable_async_tasks" }
