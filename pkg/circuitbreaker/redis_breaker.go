package circuitbreaker

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"agent-gateway/pkg/metrics"

	"github.com/redis/go-redis/v9"
)

const (
	redisKeyPrefix  = "cb:"
	windowSeconds   = 60 // 统计窗口，秒
	stateKeyTTL     = 2 * windowSeconds * time.Second
	halfOpenTimeout = 30 * time.Second
)

// RedisBreaker 分布式熔断器
// 计数器存 Redis（所有实例共享），状态机逻辑在本地。
// Redis 不可用时自动降级为本地 gobreaker，外部无感知。
type RedisBreaker struct {
	name string
	cfg  Config
	rdb  *redis.Client

	// 降级用的本地熔断器
	local *GoBreaker

	// 降级状态
	mu           sync.RWMutex
	degraded     bool
	failCount    int64 // Redis 连续失败次数（atomic）
	lastFailTime time.Time

	// 防止并发探测 Redis 恢复
	probeMu sync.Mutex
}

// NewRedisBreaker 创建分布式熔断器
func NewRedisBreaker(rdb *redis.Client, cfg Config) *RedisBreaker {
	localCfg := cfg
	localCfg.Name = cfg.Name + ":local-fallback"
	return &RedisBreaker{
		name:  cfg.Name,
		cfg:   cfg,
		rdb:   rdb,
		local: NewGoBreaker(localCfg),
	}
}

// Execute 通过熔断器执行函数
func (b *RedisBreaker) Execute(fn func() (interface{}, error)) (interface{}, error) {
	if b.isDegraded() {
		b.maybeRecover()
		return b.local.Execute(fn)
	}

	// 检查当前状态
	state, err := b.getState()
	if err != nil {
		b.recordRedisFailure()
		return b.local.Execute(fn)
	}
	b.resetRedisFailure()

	switch state {
	case StateOpen:
		return nil, ErrOpenState
	case StateHalfOpen:
		return b.executeHalfOpen(fn)
	default: // StateClosed
		return b.executeClosed(fn)
	}
}

// State 返回当前状态
func (b *RedisBreaker) State() State {
	if b.isDegraded() {
		return b.local.State()
	}
	s, err := b.getState()
	if err != nil {
		return StateClosed
	}
	return s
}

// Name 返回名称
func (b *RedisBreaker) Name() string {
	return b.name
}

// ---- 执行逻辑 ----

func (b *RedisBreaker) executeClosed(fn func() (interface{}, error)) (interface{}, error) {
	result, err := fn()
	if recordErr := b.record(err == nil); recordErr != nil {
		b.recordRedisFailure()
	} else {
		b.resetRedisFailure()
	}
	return result, err
}

func (b *RedisBreaker) executeHalfOpen(fn func() (interface{}, error)) (interface{}, error) {
	// 半开状态只放行 MaxRequests 个探测请求
	allowed, err := b.acquireHalfOpenSlot()
	if err != nil {
		b.recordRedisFailure()
		return b.local.Execute(fn)
	}
	if !allowed {
		return nil, ErrTooManyRequests
	}

	result, execErr := fn()
	if writeErr := b.record(execErr == nil); writeErr != nil {
		b.recordRedisFailure()
	} else {
		b.resetRedisFailure()
	}
	return result, execErr
}

// ---- Redis 操作 ----

// record 记录一次请求结果，并根据错误率决定是否切换状态
var recordScript = redis.NewScript(`
local base      = KEYS[1]
local window    = tonumber(ARGV[1])
local now       = tonumber(ARGV[2])
local ok        = tonumber(ARGV[3])  -- 1=success 0=failure
local minReq    = tonumber(ARGV[4])
local thresh    = tonumber(ARGV[5])  -- 0.0~1.0
local maxProbes = tonumber(ARGV[6])  -- half-open 需要全部成功的探测数

local sKey      = base .. ':s'
local fKey      = base .. ':f'
local stKey     = base .. ':state'
local probeKey  = base .. ':ho:s'   -- half-open 成功探测计数

-- 当前状态（先读，half-open 不写滑动窗口）
local state = redis.call('GET', stKey)
if not state then state = 'closed' end

if state == 'half-open' then
    if ok == 0 then
        -- 任何一个探测失败 → 立刻重新打开，重置探测计数
        redis.call('SET', stKey, 'open', 'EX', window * 2)
        redis.call('SET', base .. ':open_at', now, 'EX', window * 2)
        redis.call('DEL', base .. ':ho')
        redis.call('DEL', probeKey)
        return 'open'
    end
    -- 探测成功：累计成功次数
    local s = redis.call('INCR', probeKey)
    redis.call('EXPIRE', probeKey, 60)
    if s >= maxProbes then
        -- 所有探测全部成功 → 关闭，清空滑动窗口给恢复后的服务一个干净起点
        redis.call('SET', stKey, 'closed', 'EX', window * 2)
        redis.call('DEL', base .. ':ho')
        redis.call('DEL', probeKey)
        redis.call('DEL', base .. ':open_at')
        redis.call('DEL', sKey)
        redis.call('DEL', fKey)
        return 'closed'
    end
    return 'half-open'
end

-- closed 状态：写滑动窗口，判断是否触发熔断
local minScore = now - window * 1000
redis.call('ZREMRANGEBYSCORE', sKey, '-inf', minScore)
redis.call('ZREMRANGEBYSCORE', fKey, '-inf', minScore)

local member = tostring(now) .. '-' .. tostring(math.random(1000000))
if ok == 1 then
    redis.call('ZADD', sKey, now, member)
else
    redis.call('ZADD', fKey, now, member)
end
redis.call('PEXPIRE', sKey, window * 1000 + 5000)
redis.call('PEXPIRE', fKey, window * 1000 + 5000)

local successes = redis.call('ZCARD', sKey)
local failures  = redis.call('ZCARD', fKey)
local total = successes + failures

if total >= minReq then
    local rate = failures / total
    if rate >= thresh then
        redis.call('SET', stKey, 'open', 'EX', window * 2)
        redis.call('SET', base .. ':open_at', now, 'EX', window * 2)
        return 'open'
    end
end
return 'closed'
`)

func (b *RedisBreaker) record(success bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	ok := 0
	if success {
		ok = 1
	}
	_, err := recordScript.Run(ctx, b.rdb,
		[]string{redisKeyPrefix + b.name},
		windowSeconds,
		time.Now().UnixMilli(),
		ok,
		b.cfg.MinRequests,
		b.cfg.ErrorRateThreshold,
		b.cfg.MaxRequests,
	).Text()
	return err
}

// getState 读取当前熔断状态，并在 Open 超时后自动切换到 HalfOpen
var getStateScript = redis.NewScript(`
local base   = KEYS[1]
local now    = tonumber(ARGV[1])
local hoMs   = tonumber(ARGV[2])  -- half-open timeout ms

local stKey = base .. ':state'
local state = redis.call('GET', stKey)
if not state then return 'closed' end

if state == 'open' then
    -- 检查是否到了尝试半开的时间
    local openAt = redis.call('GET', base .. ':open_at')
    if openAt and (now - tonumber(openAt)) >= hoMs then
        redis.call('SET', stKey, 'half-open', 'EX', 120)
        return 'half-open'
    end
    return 'open'
end

return state
`)

func (b *RedisBreaker) getState() (State, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	s, err := getStateScript.Run(ctx, b.rdb,
		[]string{redisKeyPrefix + b.name},
		time.Now().UnixMilli(),
		halfOpenTimeout.Milliseconds(),
	).Text()
	if err != nil {
		return StateClosed, err
	}
	return parseState(s), nil
}

// acquireHalfOpenSlot 半开状态下获取探测名额
var halfOpenScript = redis.NewScript(`
local key = KEYS[1]
local max = tonumber(ARGV[1])
local ttl = tonumber(ARGV[2])
local count = redis.call('INCR', key)
if count == 1 then
    redis.call('EXPIRE', key, ttl)
end
if count > max then
    return 0
end
return 1
`)

func (b *RedisBreaker) acquireHalfOpenSlot() (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	result, err := halfOpenScript.Run(ctx, b.rdb,
		[]string{fmt.Sprintf("%s%s:ho", redisKeyPrefix, b.name)},
		b.cfg.MaxRequests,
		int(halfOpenTimeout.Seconds()),
	).Int()
	if err != nil {
		return false, err
	}
	return result == 1, nil
}

// ---- 降级管理 ----

func (b *RedisBreaker) isDegraded() bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.degraded
}

func (b *RedisBreaker) recordRedisFailure() {
	n := atomic.AddInt64(&b.failCount, 1)
	if n >= int64(b.cfg.MinRequests) {
		b.mu.Lock()
		if !b.degraded {
			b.degraded = true
			b.lastFailTime = time.Now()
			metrics.DegradedTotal.WithLabelValues("circuitbreaker").Inc()
		}
		b.mu.Unlock()
	}
}

func (b *RedisBreaker) resetRedisFailure() {
	atomic.StoreInt64(&b.failCount, 0)
}

func (b *RedisBreaker) maybeRecover() {
	b.mu.RLock()
	lastFail := b.lastFailTime
	b.mu.RUnlock()

	if time.Since(lastFail) < b.cfg.Timeout {
		return
	}

	go func() {
		b.probeMu.Lock()
		defer b.probeMu.Unlock()

		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer cancel()
		if b.rdb.Ping(ctx).Err() == nil {
			b.mu.Lock()
			b.degraded = false
			atomic.StoreInt64(&b.failCount, 0)
			b.mu.Unlock()
			metrics.DegradedRecoveryTotal.WithLabelValues("circuitbreaker").Inc()
		}
	}()
}

func parseState(s string) State {
	switch s {
	case "open":
		return StateOpen
	case "half-open":
		return StateHalfOpen
	default:
		return StateClosed
	}
}
