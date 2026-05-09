// Package concurrency 提供可插拔的并发控制器组件
// 支持本地、分布式、混合模式，类似于 pkg/ratelimit 的设计
package concurrency

import (
	"context"
	"errors"
	"time"
)

// ========== 错误定义 ==========

// ErrQueueTimeout 排队超时错误
var ErrQueueTimeout = errors.New("queue timeout exceeded")

// ========== 配置 ==========

// Config 并发控制器配置
type Config struct {
	// Enabled 是否启用
	Enabled bool

	// MaxConcurrency 最大并发数
	MaxConcurrency int

	// QueueTimeout 排队超时（0 = 不排队，立即返回）
	QueueTimeout time.Duration

	// 降级策略
	FallbackStrategy FallbackStrategy

	// 降级阈值
	FailureThreshold int  // 连续失败 N 次触发降级
	RecoveryTimeout  time.Duration  // 降级后多久尝试恢复

	// Redis 相关配置（分布式模式）
	RedisTimeout time.Duration  // Redis 操作超时
	KeyPrefix    string         // Redis key 前缀
	TokenTTL     time.Duration  // Token 过期时间
}

// FallbackStrategy 降级策略
type FallbackStrategy string

const (
	FallbackLocal  FallbackStrategy = "local"   // 降级到本地限流
	FallbackReject FallbackStrategy = "reject"  // 降级到直接拒绝
	FallbackNoop   FallbackStrategy = "noop"    // 降级到不限制
)

// DefaultConfig 返回默认配置
func DefaultConfig() Config {
	return Config{
		Enabled:          true,
		MaxConcurrency:    100,
		QueueTimeout:      5 * time.Second,
		FallbackStrategy:  FallbackLocal,
		FailureThreshold:  5,
		RecoveryTimeout:   30 * time.Second,
		RedisTimeout:      2 * time.Second,
		KeyPrefix:         "concur:",
		TokenTTL:          60 * time.Second,
	}
}

// normalize 规范化配置
func (c *Config) normalize() {
	if c.MaxConcurrency <= 0 {
		c.MaxConcurrency = 100
	}
	if c.QueueTimeout < 0 {
		c.QueueTimeout = 0
	}
	if c.FailureThreshold <= 0 {
		c.FailureThreshold = 5
	}
	if c.RecoveryTimeout <= 0 {
		c.RecoveryTimeout = 30 * time.Second
	}
	if c.RedisTimeout <= 0 {
		c.RedisTimeout = 2 * time.Second
	}
	if c.KeyPrefix == "" {
		c.KeyPrefix = "concur:"
	}
	if c.TokenTTL <= 0 {
		c.TokenTTL = 60 * time.Second
	}
}

// ========== 接口定义 ==========

// Controller 并发控制器接口
// 类似于 Limiter 接口设计
type Controller interface {
	// Acquire 获取并发槽位
	// 返回 release 函数（必须调用）和错误
	// 超时或满载时返回错误
	Acquire(ctx context.Context, key string) (release func(), err error)

	// Close 关闭控制器
	Close() error

	// State 返回当前状态（用于监控）
	State() State
}

// ========== 状态 ==========

// State 并发控制器状态（用于监控）
type State struct {
	// Mode 当前模式：local / distributed / degraded
	Mode string

	// Total 总槽位数
	Total int

	// Available 可用槽位数
	Available int

	// Acquired 已占用槽位数
	Acquired int

	// FallbackCount 累计降级次数
	FallbackCount int
}

// ========== 工厂函数 ==========

// NewController 根据配置创建并发控制器
func NewController(cfg Config) Controller {
	cfg.normalize()
	if !cfg.Enabled {
		return NewNoopController()
	}
	return NewLocalController(cfg)
}
