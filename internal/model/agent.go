package model

import (
	"time"

	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// AgentStatus Agent 生命周期状态
type AgentStatus int8

const (
	AgentStatusInactive AgentStatus = 0 // 心跳超时，自动摘除
	AgentStatusActive   AgentStatus = 1 // 正常服务
	AgentStatusDraining AgentStatus = 2 // 主动注销，优雅下线
)

// DeliveryMode Agent 接收任务的方式
type DeliveryMode int8

const (
	DeliveryModePush DeliveryMode = 0 // HTTP A2A server (原有模式,URL 必填)
	DeliveryModePull DeliveryMode = 1 // GAS Agent Core 主动拉取 (本地 headless 宿主,URL 可空)
)

// Agent A2A Agent 注册表
type Agent struct {
	ID          int64          `gorm:"primaryKey;autoIncrement"                                       json:"id"`
	AgentID     string         `gorm:"column:agent_id;type:varchar(128);uniqueIndex;not null"         json:"agent_id"`
	Name        string         `gorm:"column:name;type:varchar(128);not null"                         json:"name"`
	Description string         `gorm:"column:description;type:text"                                   json:"description"`
	URL         string         `gorm:"column:url;type:varchar(512)"                                   json:"url"`
	Version     string         `gorm:"column:version;type:varchar(64)"                                json:"version"`
	OwnerAppID  string         `gorm:"column:owner_app_id;type:varchar(128);index;not null"           json:"owner_app_id"`
	Status      AgentStatus    `gorm:"column:status;default:1"                                        json:"status"`
	Visibility  VisibilityMode `gorm:"column:visibility;default:0"                                    json:"visibility"` // 0=public, 1=private

	SupportsStreaming          bool `gorm:"column:supports_streaming;default:0"          json:"supports_streaming"`
	SupportsPushNotifications bool `gorm:"column:supports_push_notifications;default:0" json:"supports_push_notifications"`

	// DeliveryMode 决定消息如何投递给 Agent:
	//   0 = push (HTTP A2A server, URL 必填)
	//   1 = pull (GAS Agent Core, URL 可空)
	DeliveryMode DeliveryMode `gorm:"column:delivery_mode;type:tinyint;not null;default:1" json:"delivery_mode"`

	// 完整 AgentCard JSON，用于 /agents/:id/card 端点直接返回
	AgentCardJSON datatypes.JSON `gorm:"column:agent_card_json;type:json" json:"agent_card_json"`

	LastHeartbeatAt *time.Time     `gorm:"column:last_heartbeat_at"                json:"last_heartbeat_at"`
	DeletedAt       gorm.DeletedAt `gorm:"column:deleted_at;index"                 json:"deleted_at"`
	CreatedAt       time.Time      `gorm:"column:created_at;autoCreateTime"        json:"created_at"`
	UpdatedAt       time.Time      `gorm:"column:updated_at;autoUpdateTime"        json:"updated_at"`
}

func (Agent) TableName() string { return "agents" }
