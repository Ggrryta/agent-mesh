package model

import (
	"encoding/json"
	"time"
)

// AsyncTaskStatus 异步任务状态
type AsyncTaskStatus string

const (
	AsyncTaskStatusPending   AsyncTaskStatus = "pending"
	AsyncTaskStatusRunning   AsyncTaskStatus = "running"
	AsyncTaskStatusCompleted AsyncTaskStatus = "completed"
	AsyncTaskStatusFailed    AsyncTaskStatus = "failed"
)

// AsyncTask 异步调用任务记录（Redis 存储版）
type AsyncTask struct {
	TaskID    string          `json:"task_id"`
	AgentID   string          `json:"agent_id"`             // 目标 Agent
	SkillID   string          `json:"skill_id,omitempty"`   // 目标 AgentSkill（可选）
	AppID     string          `json:"app_id,omitempty"`
	Input     json.RawMessage `json:"input"`
	Output    json.RawMessage `json:"output,omitempty"`
	Status    AsyncTaskStatus `json:"status"`
	ErrorMsg  string          `json:"error_msg,omitempty"`
	Retries   int             `json:"retries"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
}
