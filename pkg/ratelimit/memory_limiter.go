package ratelimit

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// memoryLimiter 本地内存限流器
// 使用滑动窗口算法，单机场景使用
// 也作为分布式限流器降级时的备用限流器
type memoryLimiter struct {
	mu     sync.Mutex
	stores map[string]*timeSlot
}

type timeSlot struct {
	timestamps []int64
}

// newMemoryLimiter 创建内存限流器
func newMemoryLimiter() *memoryLimiter {
	return &memoryLimiter{
		stores: make(map[string]*timeSlot),
	}
}

// Check 检查是否允许通过
func (l *memoryLimiter) Check(ctx context.Context, key string, limit int) error {
	if limit <= 0 {
		return nil // limit 为 0 表示不限制
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now().UnixMilli()
	window := int64(1000) // 1 秒窗口
	minScore := now - window

	s, ok := l.stores[key]
	if !ok {
		s = &timeSlot{timestamps: make([]int64, 0, limit)}
		l.stores[key] = s
	}

	// 清理过期记录（滑动窗口）
	var valid []int64
	for _, ts := range s.timestamps {
		if ts > minScore {
			valid = append(valid, ts)
		}
	}

	// 检查是否超限
	if len(valid) >= limit {
		return fmt.Errorf("rate limit exceeded: %s", key)
	}

	// 记录当前请求
	s.timestamps = append(valid, now)
	return nil
}

// GetState 获取状态（内存限流器始终为 normal）
func (l *memoryLimiter) GetState() State {
	return State{Mode: "normal"}
}

// SetLocalRatio 内存限流器无分布式概念，空实现
func (l *memoryLimiter) SetLocalRatio(_ float64) {}

// cleanup 清理过期数据（可定期调用，防止内存泄漏）
func (l *memoryLimiter) cleanup() {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now().UnixMilli()
	window := int64(1000)
	minScore := now - window

	for key, s := range l.stores {
		var valid []int64
		for _, ts := range s.timestamps {
			if ts > minScore {
				valid = append(valid, ts)
			}
		}
		if len(valid) == 0 {
			delete(l.stores, key)
		} else {
			s.timestamps = valid
		}
	}
}
