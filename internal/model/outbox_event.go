package model

import (
	"time"

	"gorm.io/datatypes"
)

const (
	OutboxStatusPending = "pending"
	OutboxStatusSent    = "sent"
	OutboxStatusFailed  = "failed"

	OutboxAggregateAsyncTask = "async_task"

	OutboxEventAsyncTaskCreated = "async_task.created"
	OutboxEventAsyncTaskRetry   = "async_task.retry"
)

// OutboxEvent stores durable integration events to be published to MQ.
type OutboxEvent struct {
	ID            int64          `gorm:"primaryKey;autoIncrement" json:"id"`
	EventID       string         `gorm:"column:event_id;type:varchar(64);uniqueIndex;not null" json:"event_id"`
	AggregateType string         `gorm:"column:aggregate_type;type:varchar(64);index;not null" json:"aggregate_type"`
	AggregateID   string         `gorm:"column:aggregate_id;type:varchar(128);index;not null" json:"aggregate_id"`
	EventType     string         `gorm:"column:event_type;type:varchar(128);index;not null" json:"event_type"`
	Payload       datatypes.JSON `gorm:"column:payload;type:json;not null" json:"payload"`
	Status        string         `gorm:"column:status;type:varchar(32);index;not null;default:'pending'" json:"status"`
	Retries       int            `gorm:"column:retries;not null;default:0" json:"retries"`
	NextRetryAt   *time.Time     `gorm:"column:next_retry_at;index" json:"next_retry_at,omitempty"`
	SentAt        *time.Time     `gorm:"column:sent_at" json:"sent_at,omitempty"`
	CreatedAt     time.Time      `gorm:"column:created_at;autoCreateTime" json:"created_at"`
	UpdatedAt     time.Time      `gorm:"column:updated_at;autoUpdateTime" json:"updated_at"`
}

func (OutboxEvent) TableName() string { return "outbox_events" }
