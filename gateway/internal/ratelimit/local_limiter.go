package ratelimit

import (
	"net/http"
	"sync"
	"time"

	"github.com/Ggrryta/agent-mesh/gateway/internal/api/httpx"
	"github.com/Ggrryta/agent-mesh/gateway/internal/middleware"
	"github.com/Ggrryta/agent-mesh/gateway/internal/observability/metrics"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var rateLimitRejectTotal = promauto.NewCounterVec(prometheus.CounterOpts{
	Namespace: "mesh",
	Subsystem: "ratelimit",
	Name:      "reject_total",
	Help:      "Total requests rejected by rate limiter.",
}, []string{"agent_id"})

// Config 限流配置。
type Config struct {
	RequestsPerSecond int // per-agent 每秒请求数
	BurstSize         int // per-agent 突发上限
	GlobalRPS         int // 全局每秒请求数（0 = 不启用全局限流）
	GlobalBurst       int // 全局突发上限
}

// DefaultConfig 默认：per-agent 50 req/s 突发 100，全局 2000 req/s 突发 4000。
func DefaultConfig() Config {
	return Config{
		RequestsPerSecond: 50,
		BurstSize:         100,
		GlobalRPS:         2000,
		GlobalBurst:       4000,
	}
}

// LocalLimiter 是 per-agent 令牌桶限流器（进程内），带定时清理防止内存泄漏。
type LocalLimiter struct {
	cfg     Config
	mu      sync.Mutex
	buckets map[string]*tokenBucket
	stop    chan struct{}
}

var _ RateLimiter = (*LocalLimiter)(nil)

const bucketIdleTimeout = 10 * time.Minute

type tokenBucket struct {
	tokens     float64
	maxTokens  float64
	refillRate float64
	lastRefill time.Time
}

func NewLocalLimiter(cfg Config) *LocalLimiter {
	l := &LocalLimiter{
		cfg:     cfg,
		buckets: make(map[string]*tokenBucket),
		stop:    make(chan struct{}),
	}
	go l.cleanupLoop()
	return l
}

// Stop 停止清理协程。
func (l *LocalLimiter) Stop() {
	close(l.stop)
}

// UpdateConfig 热更新限流配置。已有桶的 rate/burst 会在下次请求时生效。
func (l *LocalLimiter) UpdateConfig(cfg Config) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.cfg = cfg
	for id, b := range l.buckets {
		if id == globalKey {
			b.refillRate = float64(cfg.GlobalRPS)
			b.maxTokens = float64(cfg.GlobalBurst)
		} else {
			b.refillRate = float64(cfg.RequestsPerSecond)
			b.maxTokens = float64(cfg.BurstSize)
		}
		if b.tokens > b.maxTokens {
			b.tokens = b.maxTokens
		}
	}
}

func (l *LocalLimiter) cleanupLoop() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			l.evictIdle()
		case <-l.stop:
			return
		}
	}
}

func (l *LocalLimiter) evictIdle() {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	for id, b := range l.buckets {
		if now.Sub(b.lastRefill) > bucketIdleTimeout {
			delete(l.buckets, id)
		}
	}
}

// Allow 判断 agentID 是否允许通过。
func (l *LocalLimiter) Allow(agentID string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	b, ok := l.buckets[agentID]
	if !ok {
		b = &tokenBucket{
			tokens:     float64(l.cfg.BurstSize),
			maxTokens:  float64(l.cfg.BurstSize),
			refillRate: float64(l.cfg.RequestsPerSecond),
			lastRefill: time.Now(),
		}
		l.buckets[agentID] = b
	}

	now := time.Now()
	elapsed := now.Sub(b.lastRefill).Seconds()
	b.tokens += elapsed * b.refillRate
	if b.tokens > b.maxTokens {
		b.tokens = b.maxTokens
	}
	b.lastRefill = now

	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

const globalKey = "__global__"

// Middleware 返回 per-agent 限流中间件。只对 mesh API（agent JWT）生效。
// 先检查全局限流，再检查 per-agent 限流。
func (l *LocalLimiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims := middleware.ClaimsFromContext(r.Context())
		if claims == nil || claims.AgentID == "" {
			next.ServeHTTP(w, r)
			return
		}

		if l.cfg.GlobalRPS > 0 && !l.allowGlobal() {
			rateLimitRejectTotal.WithLabelValues("__global__").Inc()
			httpx.WriteError(w, http.StatusTooManyRequests, 42900, "global rate limit exceeded")
			return
		}

		if !l.Allow(claims.AgentID) {
			rateLimitRejectTotal.WithLabelValues(claims.AgentID).Inc()
			httpx.WriteError(w, http.StatusTooManyRequests, 42900, "rate limit exceeded")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (l *LocalLimiter) allowGlobal() bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	b, ok := l.buckets[globalKey]
	if !ok {
		b = &tokenBucket{
			tokens:     float64(l.cfg.GlobalBurst),
			maxTokens:  float64(l.cfg.GlobalBurst),
			refillRate: float64(l.cfg.GlobalRPS),
			lastRefill: time.Now(),
		}
		l.buckets[globalKey] = b
	}

	now := time.Now()
	elapsed := now.Sub(b.lastRefill).Seconds()
	b.tokens += elapsed * b.refillRate
	if b.tokens > b.maxTokens {
		b.tokens = b.maxTokens
	}
	b.lastRefill = now

	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// 保留 import
var _ = metrics.HTTPRequestsTotal
