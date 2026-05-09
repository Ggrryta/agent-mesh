// Package ratelimit 提供可插拔的限流器实现
// 支持分布式限流（Redis）和本地限流（内存）
// 支持 Redis 故障时自动降级，外部无感知
package ratelimit

import (
	"context"
	"time"
)

// Limiter 限流器接口
// 外部调用方只关心这个接口，不关心内部实现
type Limiter interface {
	// Check 检查是否允许通过
	// key: 限流维度标识（如 "rl:cap:skill.echo.v1"）
	// limit: QPS 限制
	// 返回 nil 表示允许，返回 error 表示限流
	Check(ctx context.Context, key string, limit int) error

	// GetState 获取当前状态（用于监控）
	GetState() State

	// SetLocalRatio 动态更新本地配额比例（供 InstanceWatcher 回调使用）
	// ratio 应在 (0, 1) 范围内，通常为 1/instanceCount
	SetLocalRatio(ratio float64)
}

// State 限流器状态（用于监控和可观测性）
type State struct {
	// Mode 当前模式：normal / degraded / recovering
	Mode string `json:"mode"`

	// FallbackCount 累计降级次数
	FallbackCount int `json:"fallback_count"`

	// LastFallback 最后一次降级时间
	LastFallback time.Time `json:"last_fallback"`

	// ErrorCount 错误计数
	ErrorCount int `json:"error_count"`
}

// BackendType 限流器后端类型
type BackendType string

const (
	// BackendCluster 纯 Redis 分布式限流
	BackendCluster BackendType = "cluster"

	// BackendHybrid 混合模式（本地快速拒绝 + Redis 精确控制）
	BackendHybrid BackendType = "hybrid"

	// BackendLocal 纯本地限流
	BackendLocal BackendType = "local"
)

// Config 限流器配置
type Config struct {
	// Enabled 是否启用限流
	Enabled bool `json:"enabled" yaml:"enabled"`

	// Backend 后端类型：cluster / hybrid / local
	Backend BackendType `json:"backend" yaml:"backend"`

	// FallbackStrategy 降级策略
	FallbackStrategy FallbackStrategy `json:"fallback_strategy" yaml:"fallback_strategy"`

	// FailureThreshold Redis 故障判定阈值（连续失败次数）
	FailureThreshold int `json:"failure_threshold" yaml:"failure_threshold"`

	// RecoveryTimeout 熔断恢复时间（多久后尝试恢复）
	RecoveryTimeout time.Duration `json:"recovery_timeout" yaml:"recovery_timeout"`

	// LocalLimit 本地限流 QPS（降级时使用），0 表示使用请求的 limit
	LocalLimit int `json:"local_limit" yaml:"local_limit"`

	// LocalRatio 本地配额比例（混合模式使用），默认 0.33
	LocalRatio float64 `json:"local_ratio" yaml:"local_ratio"`
}

// FallbackStrategy 降级策略
type FallbackStrategy string

const (
	// FallbackPass 降级时放行所有请求（fail-open）
	// 适用场景：高可用优先，宁可超卖也不拒绝
	FallbackPass FallbackStrategy = "pass"

	// FallbackLocal 降级时使用本地限流（推荐）
	// 适用场景：平衡可用性和安全性
	FallbackLocal FallbackStrategy = "local"

	// FallbackReject 降级时拒绝所有请求（fail-close）
	// 适用场景：安全优先，宁可拒绝也不超卖
	FallbackReject FallbackStrategy = "reject"
)

// DefaultConfig 返回默认配置
func DefaultConfig() Config {
	return Config{
		Enabled:          true,
		Backend:          BackendHybrid, // 默认使用混合模式
		FallbackStrategy: FallbackLocal,
		FailureThreshold: 5,
		RecoveryTimeout:  30 * time.Second,
		LocalLimit:       0,
		LocalRatio:       0.33,
	}
}
