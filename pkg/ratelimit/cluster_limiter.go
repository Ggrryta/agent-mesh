package ratelimit

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"
)

// ClusterLimiter 分布式限流器
// 基于 Redis 滑动窗口算法实现分布式限流
// 支持 Redis 故障时自动降级，外部无感知
type ClusterLimiter struct {
	rdb    *redis.Client
	script *redis.Script
	config Config

	// 降级状态管理
	mu            sync.RWMutex
	degraded      bool       // 是否处于降级状态
	failureCount  int        // 连续失败计数
	lastFailTime  time.Time  // 最后一次失败时间
	lastFallback  time.Time  // 最后一次降级时间
	fallbackCount int        // 累计降级次数

	// 本地限流器（降级时使用）
	localLimiter *memoryLimiter

	// 状态原子存储（用于监控）
	atomicState atomic.Value

	// 恢复探测锁（防止并发探测）
	probeMu sync.Mutex
}

// slidingWindowLua Redis 滑动窗口限流 Lua 脚本
// 使用 Sorted Set 实现，member 为时间戳，score 也为时间戳
const slidingWindowLua = `
local key = KEYS[1]
local now = tonumber(ARGV[1])
local window = tonumber(ARGV[2])
local limit = tonumber(ARGV[3])
local min_score = now - window

redis.call('ZREMRANGEBYSCORE', key, '-inf', min_score)
local count = redis.call('ZCARD', key)
if count >= limit then
    return 1
end
redis.call('ZADD', key, now, now .. '-' .. math.random(1000000))
redis.call('PEXPIRE', key, window + 1000)
return 0
`

// NewClusterLimiter 创建分布式限流器
func NewClusterLimiter(rdb *redis.Client, cfg Config) *ClusterLimiter {
	// 设置默认值
	if cfg.FailureThreshold <= 0 {
		cfg.FailureThreshold = 5
	}
	if cfg.RecoveryTimeout <= 0 {
		cfg.RecoveryTimeout = 30 * time.Second
	}

	l := &ClusterLimiter{
		rdb:          rdb,
		script:       redis.NewScript(slidingWindowLua),
		config:       cfg,
		localLimiter: newMemoryLimiter(),
	}
	l.atomicState.Store(State{Mode: "normal"})
	return l
}

// Check 检查是否允许通过
// 外部调用方无需关心内部是 Redis 限流还是本地限流
func (l *ClusterLimiter) Check(ctx context.Context, key string, limit int) error {
	// 限流未启用，直接通过
	if !l.config.Enabled {
		return nil
	}

	// 检查是否需要尝试恢复
	if l.shouldTryRecover() {
		go l.tryRecover()
	}

	// 检查是否处于降级状态
	if l.isDegraded() {
		return l.fallbackCheck(ctx, key, limit)
	}

	// 正常 Redis 限流
	err := l.redisCheck(ctx, key, limit)
	if err != nil {
		// Redis 错误，记录失败并判断是否需要降级
		if l.recordFailure() {
			// 刚触发降级，使用降级策略
			return l.fallbackCheck(ctx, key, limit)
		}
		// 还没达到降级阈值，临时放行（避免 Redis 抖动误杀）
		return nil
	}

	// 成功，重置失败计数
	l.resetFailure()
	return nil
}

// GetState 获取当前状态（用于监控）
func (l *ClusterLimiter) GetState() State {
	return l.atomicState.Load().(State)
}

// SetLocalRatio 纯 Redis 限流器无本地层，空实现
func (l *ClusterLimiter) SetLocalRatio(_ float64) {}

// ========== 内部方法 ==========

// redisCheck Redis 限流检查
func (l *ClusterLimiter) redisCheck(ctx context.Context, key string, limit int) error {
	if limit <= 0 {
		return nil
	}

	now := time.Now().UnixMilli()
	window := int64(1000) // 1 秒窗口

	result, err := l.script.Run(ctx, l.rdb, []string{key}, now, window, limit).Int64()
	if err != nil {
		return fmt.Errorf("redis error: %w", err)
	}
	if result == 1 {
		return fmt.Errorf("rate limit exceeded: %s", key)
	}
	return nil
}

// isDegraded 是否处于降级状态
func (l *ClusterLimiter) isDegraded() bool {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.degraded
}

// shouldTryRecover 是否应该尝试恢复
func (l *ClusterLimiter) shouldTryRecover() bool {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.degraded && time.Since(l.lastFailTime) > l.config.RecoveryTimeout
}

// recordFailure 记录失败，返回是否触发降级
func (l *ClusterLimiter) recordFailure() bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.failureCount++
	l.lastFailTime = time.Now()

	// 达到阈值且未处于降级状态，触发降级
	if l.failureCount >= l.config.FailureThreshold && !l.degraded {
		l.degraded = true
		l.lastFallback = time.Now()
		l.fallbackCount++
		l.updateStateLocked("degraded")
		return true
	}
	return false
}

// resetFailure 重置失败计数
func (l *ClusterLimiter) resetFailure() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.failureCount = 0
}

// tryRecover 尝试恢复到正常模式
func (l *ClusterLimiter) tryRecover() {
	// 防止并发探测
	l.probeMu.Lock()
	defer l.probeMu.Unlock()

	l.mu.RLock()
	if !l.degraded {
		l.mu.RUnlock()
		return
	}
	l.mu.RUnlock()

	// 探测 Redis 是否恢复
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err := l.rdb.Ping(ctx).Result()
	if err == nil {
		// Redis 恢复，切换回正常模式
		l.mu.Lock()
		l.degraded = false
		l.failureCount = 0
		l.updateStateLocked("normal")
		l.mu.Unlock()
	}
}

// fallbackCheck 降级检查
func (l *ClusterLimiter) fallbackCheck(ctx context.Context, key string, limit int) error {
	switch l.config.FallbackStrategy {
	case FallbackPass:
		// 放行所有请求
		return nil

	case FallbackLocal:
		// 使用本地限流
		localLimit := l.config.LocalLimit
		if localLimit <= 0 {
			localLimit = limit // 使用请求的 limit
		}
		return l.localLimiter.Check(ctx, key, localLimit)

	case FallbackReject:
		// 拒绝所有请求
		return fmt.Errorf("rate limiter degraded, request rejected")

	default:
		// 默认放行
		return nil
	}
}

// updateStateLocked 更新监控状态（调用时已持有锁）
func (l *ClusterLimiter) updateStateLocked(mode string) {
	l.atomicState.Store(State{
		Mode:          mode,
		FallbackCount: l.fallbackCount,
		LastFallback:  l.lastFallback,
		ErrorCount:    l.failureCount,
	})
}
