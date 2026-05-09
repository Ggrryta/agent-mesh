package concurrency

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"
)

// HybridController 混合并发控制器
// 两层控制：本地快速拒绝 + Redis 精确控制
// 本地是 Redis 的本地窗口（配额分配），不是独立的池子
type HybridController struct {
	cfg  Config
	rdb  *redis.Client
	script *redis.Script

	// 本地窗口：用于快速拒绝
	// 本地配额 = MaxConcurrency * localRatio
	local     *LocalController
	localRatio float64  // 默认 0.33

	// 降级状态
	mu            sync.RWMutex
	degraded      bool
	failureCount  int
	lastFailTime  time.Time
	lastFallback  time.Time
	fallbackCount int

	// 原子状态
	atomicState atomic.Value

	// 恢复探测锁
	probeMu sync.Mutex
}

// NewHybridController 创建混合并发控制器
func NewHybridController(rdb *redis.Client, cfg Config) *HybridController {
	cfg.normalize()

	// 本地配额 = 全局配额 * 比例
	localRatio := 0.33
	localLimit := int(float64(cfg.MaxConcurrency) * localRatio)
	if localLimit < 1 {
		localLimit = 1
	}

	localCfg := cfg
	localCfg.MaxConcurrency = localLimit
	localCfg.QueueTimeout = 0 // 本地不排队，满了直接拒绝

	h := &HybridController{
		cfg:        cfg,
		rdb:        rdb,
		script:     redis.NewScript(acquireLua),
		local:      NewLocalController(localCfg),
		localRatio: localRatio,
	}
	h.atomicState.Store(State{Mode: "normal"})
	return h
}

// Acquire 获取并发槽位
// 流程：本地快速拒绝 → Redis 精确控制 → 降级兜底
func (h *HybridController) Acquire(ctx context.Context, key string) (func(), error) {
	// ═══════════════════════════════════════════════════
	// 第一层：本地快速拒绝
	// 本地配额满了 → 直接拒绝，不打 Redis
	// ═══════════════════════════════════════════════════
	if _, err := h.local.Acquire(ctx, key); err != nil {
		return nil, err
	}

	// ═══════════════════════════════════════════════════
	// 检查是否需要尝试恢复
	// ═══════════════════════════════════════════════════
	if h.shouldTryRecover() {
		go h.tryRecover()
	}

	// ═══════════════════════════════════════════════════
	// 降级模式：本地通过就放行
	// ═══════════════════════════════════════════════════
	if h.isDegraded() {
		// 降级模式下，本地配额就是最终决定
		release, _ := h.local.Acquire(ctx, key)
		return release, nil
	}

	// ═══════════════════════════════════════════════════
	// 第二层：Redis 精确控制
	// ═══════════════════════════════════════════════════
	token := fmt.Sprintf("%d-%d", time.Now().UnixNano(), time.Now().UnixMicro())

	release, err := h.redisAcquire(ctx, key, token)
	if err != nil {
		// Redis 失败，记录并判断是否降级
		if h.recordFailure() {
			// 刚触发降级，本地已通过，放行
			release, _ := h.local.Acquire(ctx, key)
			return release, nil
		}
		// 还没达到降级阈值，本地已通过，放行
		release, _ := h.local.Acquire(ctx, key)
		return release, nil
	}

	// Redis 成功，重置失败计数
	h.resetFailure()
	return release, nil
}

// Close 关闭
func (h *HybridController) Close() error {
	return nil
}

// SetLocalRatio 动态更新本地配额比例（供 InstanceWatcher 回调使用）
func (h *HybridController) SetLocalRatio(ratio float64) {
	if ratio <= 0 || ratio >= 1 {
		return
	}
	localLimit := int(float64(h.cfg.MaxConcurrency) * ratio)
	if localLimit < 1 {
		localLimit = 1
	}
	// 重建本地 semaphore（channel 容量不可变，需替换）
	newLocal := NewLocalController(Config{
		MaxConcurrency: localLimit,
		QueueTimeout:   0,
	})
	h.mu.Lock()
	h.local = newLocal
	h.localRatio = ratio
	h.mu.Unlock()
}

// State 返回状态
func (h *HybridController) State() State {
	if s, ok := h.atomicState.Load().(State); ok {
		return s
	}
	return State{Mode: "normal"}
}

// ========== Redis 操作 ==========

// redisAcquire Redis 获取槽位
func (h *HybridController) redisAcquire(ctx context.Context, key, token string) (func(), error) {
	redisCtx, cancel := context.WithTimeout(ctx, h.cfg.RedisTimeout)
	defer cancel()

	result, err := h.script.Run(redisCtx, h.rdb, []string{h.cfg.KeyPrefix + key},
		h.cfg.MaxConcurrency,
		int(h.cfg.TokenTTL.Seconds()),
		token,
	).Int()

	if err != nil {
		return nil, fmt.Errorf("redis error: %w", err)
	}

	if result == 0 {
		return nil, ErrQueueTimeout
	}

	// 成功：本地计数+1，返回合并释放
	h.localAdd()
	return func() {
		h.localDone()
		h.redisRelease(key, token)
	}, nil
}

// redisRelease Redis 释放槽位
func (h *HybridController) redisRelease(key, token string) {
	ctx, cancel := context.WithTimeout(context.Background(), h.cfg.RedisTimeout)
	defer cancel()

	releaseScript := redis.NewScript(`
		local key = KEYS[1]
		local token = ARGV[1]
		redis.call('ZREM', key, token)
	`)
	releaseScript.Run(ctx, h.rdb, []string{h.cfg.KeyPrefix + key}, token)
}

// ========== 本地计数（Redis 的镜像） ==========

// localAdd 本地计数+1（Redis 获取成功后调用）
func (h *HybridController) localAdd() {
	h.local.sem <- struct{}{}
	atomic.AddInt64(&h.local.acquired, 1)
}

// localDone 本地计数-1
func (h *HybridController) localDone() {
	atomic.AddInt64(&h.local.acquired, -1)
	<-h.local.sem
}

// ========== 降级管理（对齐 HybridLimiter） ==========

// isDegraded 是否降级
func (h *HybridController) isDegraded() bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.degraded
}

// shouldTryRecover 是否应该尝试恢复
func (h *HybridController) shouldTryRecover() bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.degraded && time.Since(h.lastFailTime) > h.cfg.RecoveryTimeout
}

// recordFailure 记录失败，返回是否触发降级
func (h *HybridController) recordFailure() bool {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.failureCount++
	h.lastFailTime = time.Now()

	if h.failureCount >= h.cfg.FailureThreshold && !h.degraded {
		h.degraded = true
		h.lastFallback = time.Now()
		h.fallbackCount++
		h.updateStateLocked("degraded")
		return true
	}
	return false
}

// resetFailure 重置失败计数
func (h *HybridController) resetFailure() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.failureCount = 0
}

// tryRecover 尝试恢复
func (h *HybridController) tryRecover() {
	h.probeMu.Lock()
	defer h.probeMu.Unlock()

	h.mu.RLock()
	if !h.degraded {
		h.mu.RUnlock()
		return
	}
	h.mu.RUnlock()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if h.rdb.Ping(ctx).Err() == nil {
		h.mu.Lock()
		h.degraded = false
		h.failureCount = 0
		h.updateStateLocked("normal")
		h.mu.Unlock()
	}
}

// updateStateLocked 更新监控状态
func (h *HybridController) updateStateLocked(mode string) {
	h.atomicState.Store(State{
		Mode:          mode,
		Total:         h.cfg.MaxConcurrency,
		Available:     h.cfg.MaxConcurrency - int(atomic.LoadInt64(&h.local.acquired)),
		Acquired:      int(atomic.LoadInt64(&h.local.acquired)),
		FallbackCount: h.fallbackCount,
	})
}

// ========== Lua 脚本 ==========

// acquireLua 获取槽位脚本
const acquireLua = `
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
`
