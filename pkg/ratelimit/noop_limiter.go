package ratelimit

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"
)

// noopLimiter 空实现限流器
// 当限流功能禁用时使用，所有请求都放行
type noopLimiter struct{}

// newNoopLimiter 创建空实现限流器
func newNoopLimiter() *noopLimiter {
	return &noopLimiter{}
}

// Check 总是返回 nil（放行）
func (l *noopLimiter) Check(ctx context.Context, key string, limit int) error {
	return nil
}

// GetState 返回禁用状态
func (l *noopLimiter) GetState() State {
	return State{Mode: "disabled"}
}

// SetLocalRatio 空实现
func (l *noopLimiter) SetLocalRatio(_ float64) {}

// ========== 工厂函数 ==========

// NewLimiter 根据配置创建限流器
// 根据 Backend 类型选择具体实现
func NewLimiter(rdb *redis.Client, cfg Config) Limiter {
	if !cfg.Enabled {
		return newNoopLimiter()
	}

	switch cfg.Backend {
	case BackendLocal:
		return newMemoryLimiter()

	case BackendCluster:
		if rdb == nil {
			return newMemoryLimiter()
		}
		return NewClusterLimiter(rdb, cfg)

	case BackendHybrid:
		if rdb == nil {
			return newMemoryLimiter()
		}
		return NewHybridLimiter(rdb, cfg)

	default:
		// 默认使用混合模式
		if rdb != nil {
			return NewHybridLimiter(rdb, cfg)
		}
		return newMemoryLimiter()
	}
}

// NewLimiterWithFallback 创建限流器（带 Redis 可用性检查）
func NewLimiterWithFallback(rdb *redis.Client, cfg Config) Limiter {
	if !cfg.Enabled {
		return newNoopLimiter()
	}

	// 检查 Redis 可用性
	if rdb != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := rdb.Ping(ctx).Err(); err != nil {
			// Redis 不可用，降级到本地
			cfg.Backend = BackendLocal
		}
	}

	return NewLimiter(rdb, cfg)
}

// NewMemoryLimiter 创建纯内存限流器
// 适用于单机场景或测试
func NewMemoryLimiter() Limiter {
	return newMemoryLimiter()
}

// NewNoopLimiter 创建空实现限流器
// 适用于禁用限流场景
func NewNoopLimiter() Limiter {
	return newNoopLimiter()
}
