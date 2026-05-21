package ratelimit

import "net/http"

// RateLimiter 是限流器的统一接口。
// 本地实现（LocalLimiter）和分布式实现（RedisLimiter）都满足此接口。
type RateLimiter interface {
	Allow(agentID string) bool
	Middleware(next http.Handler) http.Handler
	UpdateConfig(cfg Config)
	Stop()
}
