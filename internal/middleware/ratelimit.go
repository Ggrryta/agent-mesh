package middleware

import (
	"context"
	"fmt"

	"agent-gateway/config"
	"agent-gateway/pkg/ratelimit"
	"agent-gateway/pkg/resp"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
)

// RateLimiter 限流器接口（使用独立限流器包）
type RateLimiter interface {
	Check(ctx context.Context, key string, limit int) error
}

// RateLimit 双维度限流中间件（agent + consumer）
// 使用独立的 ratelimit 包，支持分布式限流和自动降级
func RateLimit(limiter RateLimiter) app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		// 每次请求读取最新配置（支持热更新）
		cfg := config.GetRateLimitConfig()

		if !cfg.Enabled {
			c.Next(ctx)
			return
		}

		agentID := c.Param("agent_id")
		appID, _ := c.Get(ContextKeyAppID)
		consumerAppID, _ := appID.(string)

		// agent 维度限流
		capQPS := cfg.DefaultQPS
		if v, ok := cfg.Capability[agentID]; ok && v > 0 {
			capQPS = v
		}
		capKey := fmt.Sprintf("rl:agent:%s", agentID)
		if err := limiter.Check(ctx, capKey, capQPS); err != nil {
			c.JSON(consts.StatusTooManyRequests, resp.Err(resp.CodeTooManyRequests, "agent rate limit exceeded: "+agentID))
			c.Abort()
			return
		}

		// consumer 维度限流
		if consumerAppID != "" {
			conQPS := cfg.DefaultQPS
			if v, ok := cfg.Consumer[consumerAppID]; ok && v > 0 {
				conQPS = v
			}
			conKey := fmt.Sprintf("rl:con:%s", consumerAppID)
			if err := limiter.Check(ctx, conKey, conQPS); err != nil {
				c.JSON(consts.StatusTooManyRequests, resp.Err(resp.CodeTooManyRequests, "consumer rate limit exceeded: "+consumerAppID))
				c.Abort()
				return
			}
		}

		c.Next(ctx)
	}
}

// CheckDualLimit 暴露双维度限流函数（供 MCP Handler 等非中间件场景使用）
func CheckDualLimit(ctx context.Context, limiter RateLimiter, agentID, consumerAppID string) error {
	cfg := config.GetRateLimitConfig()
	if !cfg.Enabled {
		return nil
	}

	// agent 维度
	capQPS := cfg.DefaultQPS
	if v, ok := cfg.Capability[agentID]; ok && v > 0 {
		capQPS = v
	}
	capKey := fmt.Sprintf("rl:agent:%s", agentID)
	if err := limiter.Check(ctx, capKey, capQPS); err != nil {
		return err
	}

	// consumer 维度
	if consumerAppID != "" {
		conQPS := cfg.DefaultQPS
		if v, ok := cfg.Consumer[consumerAppID]; ok && v > 0 {
			conQPS = v
		}
		conKey := fmt.Sprintf("rl:con:%s", consumerAppID)
		if err := limiter.Check(ctx, conKey, conQPS); err != nil {
			return err
		}
	}

	return nil
}

// 确保 ratelimit.Limiter 满足我们的 RateLimiter 接口
var _ RateLimiter = (ratelimit.Limiter)(nil)
