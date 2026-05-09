package model

import "time"

const APIKeyPrefix = "agw_"

// APIKey 账号的 API Key，一个 app_id 只允许一条记录
type APIKey struct {
	ID          int64      `gorm:"primaryKey;autoIncrement" json:"id"`
	AppID       string     `gorm:"column:app_id;type:varchar(128);uniqueIndex;not null" json:"app_id"`
	KeyHash     string     `gorm:"column:key_hash;type:varchar(256);not null" json:"-"`
	KeyPrefix   string     `gorm:"column:key_prefix;type:varchar(16);uniqueIndex;not null" json:"key_prefix"`
	CreatedAt   time.Time  `gorm:"column:created_at;autoCreateTime" json:"created_at"`
	LastUsedAt  *time.Time `gorm:"column:last_used_at" json:"last_used_at"`
}

func (APIKey) TableName() string {
	return "api_keys"
}
