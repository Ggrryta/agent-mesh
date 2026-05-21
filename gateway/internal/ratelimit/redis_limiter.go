package ratelimit

import (
	"context"
	"net/http"
	"time"

	"github.com/Ggrryta/agent-mesh/gateway/internal/api/httpx"
	"github.com/Ggrryta/agent-mesh/gateway/internal/middleware"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	goredis "github.com/redis/go-redis/v9"
	"github.com/sony/gobreaker"
	"go.uber.org/zap"
)

var rateLimitFallbackTotal = promauto.NewCounter(prometheus.CounterOpts{
	Namespace: "mesh",
	Subsystem: "ratelimit",
	Name:      "fallback_total",
	Help:      "Total requests that fell back to local limiter due to Redis failure.",
})

var redisBreakerState = promauto.NewGauge(prometheus.GaugeOpts{
	Namespace: "mesh",
	Subsystem: "ratelimit",
	Name:      "redis_breaker_state",
	Help:      "Redis circuit breaker state (0=closed, 1=half-open, 2=open).",
})

const (
	redisKeyPrefix = "ratelimit:"
	redisKeyTTL    = 600 // seconds (10 min)
	redisTimeout   = 5 * time.Millisecond
)

// tokenBucketScript 是 Redis Lua 令牌桶脚本。
// KEYS[1] = ratelimit:{agentID}
// ARGV[1] = refillRate (tokens/sec)
// ARGV[2] = maxTokens (burst)
// ARGV[3] = now (unix milliseconds)
// ARGV[4] = key TTL (seconds)
// 返回 1 = allowed, 0 = rejected
var tokenBucketScript = goredis.NewScript(`
local key = KEYS[1]
local rate = tonumber(ARGV[1])
local burst = tonumber(ARGV[2])
local now = tonumber(ARGV[3])
local ttl = tonumber(ARGV[4])

local data = redis.call('HMGET', key, 'tokens', 'ts')
local tokens = tonumber(data[1]) or burst
local last_ts = tonumber(data[2]) or now

local elapsed = (now - last_ts) / 1000.0
tokens = math.min(burst, tokens + elapsed * rate)

local allowed = 0
if tokens >= 1 then
    tokens = tokens - 1
    allowed = 1
end

redis.call('HMSET', key, 'tokens', tostring(tokens), 'ts', tostring(now))
redis.call('EXPIRE', key, ttl)
return allowed
`)

// RedisLimiter 是基于 Redis 的分布式令牌桶限流器。
// Redis 不可用时通过熔断器快速降级到本地限流，避免每次请求都等超时。
type RedisLimiter struct {
	rdb      *goredis.Client
	cfg      Config
	fallback *LocalLimiter
	breaker  *gobreaker.CircuitBreaker
	log      *zap.Logger
}

var _ RateLimiter = (*RedisLimiter)(nil)

func NewRedisLimiter(rdb *goredis.Client, cfg Config, log *zap.Logger) *RedisLimiter {
	if log == nil {
		log = zap.NewNop()
	}
	cb := gobreaker.NewCircuitBreaker(gobreaker.Settings{
		Name:        "redis-ratelimit",
		MaxRequests: 1,              // 半开时只放 1 个探测请求
		Interval:    60 * time.Second, // 统计窗口
		Timeout:     10 * time.Second, // 熔断后 10s 进入半开
		ReadyToTrip: func(counts gobreaker.Counts) bool {
			return counts.ConsecutiveFailures >= 3 // 连续 3 次失败触发熔断
		},
		OnStateChange: func(name string, from, to gobreaker.State) {
			redisBreakerState.Set(float64(to))
			log.Warn("redis ratelimit breaker state changed",
				zap.String("from", from.String()),
				zap.String("to", to.String()))
		},
	})
	return &RedisLimiter{
		rdb:      rdb,
		cfg:      cfg,
		fallback: NewLocalLimiter(cfg),
		breaker:  cb,
		log:      log,
	}
}

func (l *RedisLimiter) Allow(agentID string) bool {
	result, err := l.breaker.Execute(func() (any, error) {
		ctx, cancel := context.WithTimeout(context.Background(), redisTimeout)
		defer cancel()
		now := time.Now().UnixMilli()
		key := redisKeyPrefix + agentID
		return tokenBucketScript.Run(ctx, l.rdb, []string{key},
			l.cfg.RequestsPerSecond,
			l.cfg.BurstSize,
			now,
			redisKeyTTL,
		).Int()
	})

	if err != nil {
		rateLimitFallbackTotal.Inc()
		return l.fallback.Allow(agentID)
	}
	return result.(int) == 1
}

func (l *RedisLimiter) allowGlobal() bool {
	result, err := l.breaker.Execute(func() (any, error) {
		ctx, cancel := context.WithTimeout(context.Background(), redisTimeout)
		defer cancel()
		now := time.Now().UnixMilli()
		key := redisKeyPrefix + globalKey
		return tokenBucketScript.Run(ctx, l.rdb, []string{key},
			l.cfg.GlobalRPS,
			l.cfg.GlobalBurst,
			now,
			redisKeyTTL,
		).Int()
	})

	if err != nil {
		rateLimitFallbackTotal.Inc()
		return l.fallback.allowGlobal()
	}
	return result.(int) == 1
}

func (l *RedisLimiter) Middleware(next http.Handler) http.Handler {
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

func (l *RedisLimiter) Stop() {
	l.fallback.Stop()
}

// UpdateConfig 热更新限流配置。Redis 模式下参数每次请求时传入 Lua 脚本，立即生效。
func (l *RedisLimiter) UpdateConfig(cfg Config) {
	l.cfg = cfg
	l.fallback.UpdateConfig(cfg)
}
