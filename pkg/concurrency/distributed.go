package concurrency

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"
)

// DistributedController 分布式并发控制器
// 使用 Redis Lua 脚本实现分布式信号量
type DistributedController struct {
	cfg *Config
	rdb *redis.Client

	// 状态
	fallbackCount int64
	failCount    int64  // 连续失败次数
	mode         atomic.Value  // normal / degraded
}

// NewDistributedController 创建分布式并发控制器
func NewDistributedController(rdb *redis.Client, cfg Config) *DistributedController {
	cfg.normalize()
	return &DistributedController{
		cfg:  &cfg,
		rdb: rdb,
	}
}

// Acquire 获取分布式并发槽位
func (c *DistributedController) Acquire(ctx context.Context, key string) (func(), error) {
	// 如果处于降级模式，尝试本地获取
	if c.getMode() == "degraded" {
		return c.acquireDegraded(ctx, key)
	}

	// 正常模式：尝试获取分布式槽位
	token := fmt.Sprintf("%d-%d", time.Now().UnixNano(), time.Now().UnixMicro())

	redisCtx, cancel := context.WithTimeout(ctx, c.cfg.RedisTimeout)
	defer cancel()

	// Lua 脚本：原子获取槽位
	script := redis.NewScript(`
		local key = KEYS[1]
		local max = tonumber(ARGV[1])
		local ttl = tonumber(ARGV[2])
		local token = ARGV[3]

		-- 获取当前计数
		local count = redis.call('ZCARD', key)
		
		-- 检查是否满
		if count >= max then
			return 0
		end
		
		-- 添加 token
		redis.call('ZADD', key, 0, token)
		redis.call('EXPIRE', key, ttl)
		
		return 1
	`)

	result, err := script.Run(redisCtx, c.rdb, []string{c.cfg.KeyPrefix + key},
		c.cfg.MaxConcurrency,
		int(c.cfg.TokenTTL.Seconds()),
		token,
	).Int()

	if err != nil {
		// Redis 错误，记录并降级
		atomic.AddInt64(&c.failCount, 1)
		if atomic.LoadInt64(&c.failCount) >= int64(c.cfg.FailureThreshold) {
			c.setMode("degraded")
		}
		return nil, fmt.Errorf("redis error: %w", err)
	}

	// 重置失败计数
	atomic.StoreInt64(&c.failCount, 0)

	if result == 0 {
		// 槽位已满
		if c.cfg.QueueTimeout > 0 {
			// 排队等待
			return c.waitAndRetry(ctx, key, token)
		}
		return nil, ErrQueueTimeout
	}

	// 成功获取槽位
	return func() {
		c.release(key, token)
	}, nil
}

// waitAndRetry 等待并重试
func (c *DistributedController) waitAndRetry(ctx context.Context, key, token string) (func(), error) {
	deadline := time.Now().Add(c.cfg.QueueTimeout)

	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			c.release(key, token)
			return nil, ctx.Err()
		case <-time.After(100 * time.Millisecond):
			// 尝试重新获取
			redisCtx, cancel := context.WithTimeout(ctx, c.cfg.RedisTimeout)
			script := redis.NewScript(`
				local key = KEYS[1]
				local max = tonumber(ARGV[1])
				local count = redis.call('ZCARD', key)
				if count < max then
					return 1
				end
				return 0
			`)
			result, _ := script.Run(redisCtx, c.rdb, []string{c.cfg.KeyPrefix + key}, c.cfg.MaxConcurrency).Int()
			cancel()

			if result == 1 {
				return func() {
					c.release(key, token)
				}, nil
			}
		}
	}

	// 超时
	c.release(key, token)
	return nil, ErrQueueTimeout
}

// release 释放槽位
func (c *DistributedController) release(key, token string) {
	redisCtx, cancel := context.WithTimeout(context.Background(), c.cfg.RedisTimeout)
	defer cancel()

	script := redis.NewScript(`
		local key = KEYS[1]
		local token = ARGV[1]
		redis.call('ZREM', key, token)
	`)
	script.Run(redisCtx, c.rdb, []string{c.cfg.KeyPrefix + key}, token)
}

// acquireDegraded 降级模式获取
func (c *DistributedController) acquireDegraded(ctx context.Context, key string) (func(), error) {
	// 检查是否需要恢复
	select {
	case <-time.After(c.cfg.RecoveryTimeout):
		// 尝试恢复
		redisCtx, cancel := context.WithTimeout(ctx, c.cfg.RedisTimeout)
		err := c.rdb.Ping(redisCtx).Err()
		cancel()

		if err == nil {
			c.setMode("normal")
			atomic.StoreInt64(&c.failCount, 0)
			return c.Acquire(ctx, key)
		}
	default:
	}

	// 降级模式：直接拒绝
	return nil, errors.New("concurrency control degraded")
}

// Close 关闭
func (c *DistributedController) Close() error {
	return nil
}

// State 返回状态
func (c *DistributedController) State() State {
	ctx, cancel := context.WithTimeout(context.Background(), c.cfg.RedisTimeout)
	defer cancel()

	key := c.cfg.KeyPrefix + "state"
	count, _ := c.rdb.ZCard(ctx, key).Result()

	return State{
		Mode:           c.getMode(),
		Total:          c.cfg.MaxConcurrency,
		Available:      c.cfg.MaxConcurrency - int(count),
		Acquired:       int(count),
		FallbackCount:  int(atomic.LoadInt64(&c.fallbackCount)),
	}
}

// getMode 获取模式
func (c *DistributedController) getMode() string {
	if m, ok := c.mode.Load().(string); ok {
		return m
	}
	return "normal"
}

// setMode 设置模式
func (c *DistributedController) setMode(mode string) {
	c.mode.Store(mode)
}

// setFailCount 设置失败计数
func (c *DistributedController) setFailCount(count int64) {
	atomic.StoreInt64(&c.failCount, count)
}

// setFallbackCount 设置降级计数
func (c *DistributedController) setFallbackCount(count int64) {
	atomic.StoreInt64(&c.fallbackCount, count)
}
