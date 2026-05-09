package model

import (
	"time"

	"gorm.io/gorm"
)

// Consumer 调用方（消费者）。授权范围由 agent_permissions 表决定。
type Consumer struct {
	ID          int64          `gorm:"primaryKey;autoIncrement" json:"id"`
	AppID       string         `gorm:"column:app_id;type:varchar(128);uniqueIndex;not null" json:"app_id"`
	SecretHash  string         `gorm:"column:secret_hash;type:varchar(256);not null" json:"-"` // bcrypt hash，不对外暴露
	Description string         `gorm:"column:description;type:varchar(256)" json:"description"`
	DeletedAt   gorm.DeletedAt `gorm:"column:deleted_at;index" json:"deleted_at"`
	CreatedAt   time.Time      `gorm:"column:created_at;autoCreateTime" json:"created_at"`
	UpdatedAt   time.Time      `gorm:"column:updated_at;autoUpdateTime" json:"updated_at"`
}

func (Consumer) TableName() string {
	return "consumers"
}
