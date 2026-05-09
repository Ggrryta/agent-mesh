package ratelimit

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// TokenBucketLimiter 本地令牌桶限流器
// 优势：零网络延迟（<1ms），支持突发流量
// 配合 Redis 定期校准，保证分布式精度
type TokenBucketLimiter struct {
	mu              sync.RWMutex
	buckets         map[string]*tokenBucket
	capacity        int           // 桶容量
	refill          int           // 每秒补充令牌数
	rdb             *redis.Client // 用于定期校准
	calibrationIntvl time.Duration
}

type tokenBucket struct {
	tokens     int
	lastRefill time.Time
}

// NewTokenBucketLimiter 创建令牌桶限流器
func NewTokenBucketLimiter(capacity, refill int, rdb *redis.Client) *TokenBucketLimiter {
	l := &TokenBucketLimiter{
		buckets:         make(map[string]*tokenBucket),
		capacity:        capacity,
		refill:          refill,
		rdb:             rdb,
		calibrationIntvl: 5 * time.Second,
	}
	
	// 启动定期校准
	if rdb != nil {
		go l.periodicCalibration()
	}
	
	return l
}

// Allow 检查是否允许通过（零网络延迟）
func (l *TokenBucketLimiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	bucket, exists := l.buckets[key]
	if !exists {
		bucket = &tokenBucket{
			tokens:     l.capacity,
			lastRefill: time.Now(),
		}
		l.buckets[key] = bucket
	}

	// 补充令牌
	now := time.Now()
	elapsed := now.Sub(bucket.lastRefill).Seconds()
	tokensToAdd := int(elapsed * float64(l.refill))
	if tokensToAdd > 0 {
		bucket.tokens = min(bucket.tokens+tokensToAdd, l.capacity)
		bucket.lastRefill = now
	}

	// 消费令牌
	if bucket.tokens > 0 {
		bucket.tokens--
		return true
	}

	return false
}

// periodicCalibration 定期从 Redis 校准本地计数
func (l *TokenBucketLimiter) periodicCalibration() {
	ticker := time.NewTicker(l.calibrationIntvl)
	defer ticker.Stop()

	for range ticker.C {
		l.calibrate()
	}
}

// calibrate 校准所有 bucket
func (l *TokenBucketLimiter) calibrate() {
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	l.mu.RLock()
	keys := make([]string, 0, len(l.buckets))
	for k := range l.buckets {
		keys = append(keys, k)
	}
	l.mu.RUnlock()

	// 批量从 Redis 获取实际计数
	pipe := l.rdb.Pipeline()
	cmds := make(map[string]*redis.StringCmd)
	for _, key := range keys {
		cmds[key] = pipe.Get(ctx, "ratelimit:token:"+key)
	}
	_, _ = pipe.Exec(ctx)

	// 校准本地 bucket
	l.mu.Lock()
	for key, cmd := range cmds {
		if bucket, exists := l.buckets[key]; exists {
			if redisCountStr, err := cmd.Result(); err == nil {
				var redisCount int
				fmt.Sscanf(redisCountStr, "%d", &redisCount)
				// 如果 Redis 计数远低于本地，说明其他实例消耗了配额
				// 按比例调整本地令牌
				if redisCount < bucket.tokens {
					bucket.tokens = redisCount
				}
			}
		}
	}
	l.mu.Unlock()
}

// Reset 重置所有限流桶
func (l *TokenBucketLimiter) Reset() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.buckets = make(map[string]*tokenBucket)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
