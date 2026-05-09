// Package model 数据模型定义
package model

import "time"

// ConfigType 配置类型枚举
type ConfigType string

const (
	// ConfigTypeRateLimit 限流配置
	ConfigTypeRateLimit ConfigType = "rate_limit"
	// ConfigTypeLog 日志配置
	ConfigTypeLog ConfigType = "log"
	// ConfigTypeTimeout 超时配置
	ConfigTypeTimeout ConfigType = "timeout"
	// ConfigTypeConcurrency 并发控制配置
	ConfigTypeConcurrency ConfigType = "concurrency"
	// ConfigTypeCircuitBreaker 熔断器配置
	ConfigTypeCircuitBreaker ConfigType = "circuit_breaker"
)

// ConfigVersion 配置版本表模型
// 用于配置热更新的版本管理和审计追溯
type ConfigVersion struct {
	ID            int64      `gorm:"primaryKey;autoIncrement" json:"id"`
	Version       string     `gorm:"column:version;type:varchar(64);uniqueIndex;not null" json:"version"`
	ConfigType    ConfigType `gorm:"column:config_type;type:varchar(32);not null;index" json:"config_type"`
	ConfigJSON    string     `gorm:"column:config_json;type:json;not null" json:"config_json"`
	ChangeSummary string     `gorm:"column:change_summary;type:varchar(512)" json:"change_summary"`
	CreatedAt     time.Time  `gorm:"column:created_at;not null" json:"created_at"`
	CreatedBy     string     `gorm:"column:created_by;type:varchar(128)" json:"created_by"`
}

// TableName 指定表名
func (ConfigVersion) TableName() string {
	return "config_versions"
}

// RateLimitConfigJSON 限流配置 JSON 结构（用于序列化/反序列化）
type RateLimitConfigJSON struct {
	DefaultQPS int            `json:"default_qps"`
	Enabled    bool           `json:"enabled"`
	Capability map[string]int `json:"capability"`
	Consumer   map[string]int `json:"consumer"`
}

// LogConfigJSON 日志配置 JSON 结构
type LogConfigJSON struct {
	Level  string `json:"level"`
	Format string `json:"format"`
}

// TimeoutConfigJSON 超时配置 JSON 结构（四层超时）
type TimeoutConfigJSON struct {
	Global     int `json:"global_ms"`
	Redis      int `json:"redis_ms"`
	Queue      int `json:"queue_ms"`
	Downstream int `json:"downstream_ms"`
}

// ConcurrencyConfigJSON 并发控制配置 JSON 结构
type ConcurrencyConfigJSON struct {
	MaxConcurrency   int `json:"max_concurrency"`
	FailureThreshold int `json:"failure_threshold"`
	RecoveryTimeoutS int `json:"recovery_timeout_s"`
}

// CircuitBreakerConfigJSON 熔断器配置 JSON 结构
type CircuitBreakerConfigJSON struct {
	ErrorRateThreshold float64 `json:"error_rate_threshold"`
	MinRequests        int     `json:"min_requests"`
	RecoveryIntervalS  int     `json:"recovery_interval_s"`
	MaxRequests        int     `json:"max_requests"`
}
