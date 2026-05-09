package ratelimit

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"agent-gateway/pkg/metrics"

	"github.com/redis/go-redis/v9"
)

// HybridLimiter 混合限流器
// 两层控制：本地快速拒绝 + Redis 精确控制
// 优点：减少 Redis 压力，降低延迟
type HybridLimiter struct {
	// 本地限流器（第一层：快速拒绝）
	local *memoryLimiter

	// Redis 相关
	rdb    *redis.Client
	script *redis.Script
	config Config

	// 本地配额比例（默认 0.33，即 33%）
	localRatio float64

	// 降级状态管理
	mu            sync.RWMutex
	degraded      bool
	failureCount  int
	lastFailTime  time.Time
	lastFallback  time.Time
	fallbackCount int

	// 状态原子存储
	atomicState atomic.Value

	// 恢复探测锁
	probeMu sync.Mutex
}

// NewHybridLimiter 创建混合限流器
func NewHybridLimiter(rdb *redis.Client, cfg Config) *HybridLimiter {
	// 设置默认值
	if cfg.FailureThreshold <= 0 {
		cfg.FailureThreshold = 5
	}
	if cfg.RecoveryTimeout <= 0 {
		cfg.RecoveryTimeout = 30 * time.Second
	}

	l := &HybridLimiter{
		rdb:        rdb,
		script:     redis.NewScript(slidingWindowLua),
		config:     cfg,
		local:      newMemoryLimiter(),
		localRatio: 0.33, // 默认本地配额 33%
	}
	l.atomicState.Store(State{Mode: "normal"})
	return l
}

// Check 检查是否允许通过
// 流程：本地快速拒绝 → Redis 精确控制
func (l *HybridLimiter) Check(ctx context.Context, key string, limit int) error {
	// 限流未启用，直接通过
	if !l.config.Enabled {
		return nil
	}

	if limit <= 0 {
		return nil
	}

	// ═══════════════════════════════════════════════════════════════
	// 第一层：本地快速拒绝
	// ═══════════════════════════════════════════════════════════════
	localLimit := l.calcLocalLimit(limit)
	if err := l.local.Check(ctx, key, localLimit); err != nil {
		// 本地已超限，立即拒绝（无网络开销）
		return err
	}

	// ═══════════════════════════════════════════════════════════════
	// 检查是否需要尝试恢复
	// ═══════════════════════════════════════════════════════════════
	if l.shouldTryRecover() {
		go l.tryRecover()
	}

	// ═══════════════════════════════════════════════════════════════
	// 检查是否处于降级状态
	// ═══════════════════════════════════════════════════════════════
	if l.isDegraded() {
		// 降级模式：本地通过就放行
		return nil
	}

	// ═══════════════════════════════════════════════════════════════
	// 第二层：Redis 精确控制
	// ═══════════════════════════════════════════════════════════════
	err := l.redisCheck(ctx, key, limit)
	if err != nil {
		// Redis 错误，记录失败并判断是否需要降级
		if l.recordFailure() {
			// 刚触发降级，放行（本地已通过）
			return nil
		}
		// 还没达到降级阈值，放行
		return nil
	}

	// 成功，重置失败计数
	l.resetFailure()
	return nil
}

// GetState 获取当前状态
func (l *HybridLimiter) GetState() State {
	return l.atomicState.Load().(State)
}

// SetLocalRatio 设置本地配额比例
func (l *HybridLimiter) SetLocalRatio(ratio float64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if ratio > 0 && ratio < 1 {
		l.localRatio = ratio
	}
}

// ========== 内部方法 ==========

// calcLocalLimit 计算本地配额
func (l *HybridLimiter) calcLocalLimit(limit int) int {
	l.mu.RLock()
	ratio := l.localRatio
	l.mu.RUnlock()

	localLimit := int(float64(limit) * ratio)
	if localLimit < 1 {
		localLimit = 1
	}
	return localLimit
}

// redisCheck Redis 限流检查
func (l *HybridLimiter) redisCheck(ctx context.Context, key string, limit int) error {
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
func (l *HybridLimiter) isDegraded() bool {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.degraded
}

// shouldTryRecover 是否应该尝试恢复
func (l *HybridLimiter) shouldTryRecover() bool {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.degraded && time.Since(l.lastFailTime) > l.config.RecoveryTimeout
}

// recordFailure 记录失败，返回是否触发降级
func (l *HybridLimiter) recordFailure() bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.failureCount++
	l.lastFailTime = time.Now()

	if l.failureCount >= l.config.FailureThreshold && !l.degraded {
		l.degraded = true
		l.lastFallback = time.Now()
		l.fallbackCount++
		l.updateStateLocked("degraded")
		metrics.DegradedTotal.WithLabelValues("ratelimit").Inc()
		return true
	}
	return false
}

// resetFailure 重置失败计数
func (l *HybridLimiter) resetFailure() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.failureCount = 0
}

// tryRecover 尝试恢复到正常模式
func (l *HybridLimiter) tryRecover() {
	l.probeMu.Lock()
	defer l.probeMu.Unlock()

	l.mu.RLock()
	if !l.degraded {
		l.mu.RUnlock()
		return
	}
	l.mu.RUnlock()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err := l.rdb.Ping(ctx).Result()
	if err == nil {
		l.mu.Lock()
		l.degraded = false
		l.failureCount = 0
		l.updateStateLocked("normal")
		l.mu.Unlock()
		metrics.DegradedRecoveryTotal.WithLabelValues("ratelimit").Inc()
	}
}

// updateStateLocked 更新监控状态
func (l *HybridLimiter) updateStateLocked(mode string) {
	l.atomicState.Store(State{
		Mode:          mode,
		FallbackCount: l.fallbackCount,
		LastFallback:  l.lastFallback,
		ErrorCount:    l.failureCount,
	})
}
