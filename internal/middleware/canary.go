package middleware

import (
	"context"
	"math/rand"

	"agent-gateway/pkg/logger"

	"github.com/cloudwego/hertz/pkg/app"
	"go.uber.org/zap"
)

// CanaryRouter 灰度流量路由中间件
// 根据 Header 或随机权重将请求路由到灰度实例
func CanaryRouter(canaryEnabled bool, canaryWeight int) app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		if !canaryEnabled {
			c.Next(ctx)
			return
		}

		// 1. 优先检查显式灰度标记
		canaryHeader := string(c.GetHeader("X-Canary"))
		if canaryHeader == "true" || canaryHeader == "beta" {
			c.Set("is_canary", true)
			logger.Ctx(ctx).Debug("canary routing: explicit header match")
			c.Next(ctx)
			return
		}

		// 2. 按权重随机分配
		if canaryWeight > 0 && canaryWeight < 100 {
			randVal := rand.Intn(100)
			if randVal < canaryWeight {
				c.Set("is_canary", true)
				logger.Ctx(ctx).Debug("canary routing: weight match",
					zap.Int("weight", canaryWeight),
					zap.Int("rand", randVal))
				c.Next(ctx)
				return
			}
		}

		// 3. 默认路由到正常实例
		c.Set("is_canary", false)
		c.Next(ctx)
	}
}

// CanaryMetrics 灰度指标记录中间件
// 记录正常/灰度实例的请求指标，用于对比分析
func CanaryMetrics() app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		isCanary, _ := c.Get("is_canary")
		
		// 在 context 中存储灰度标记，供后续使用
		ctx = context.WithValue(ctx, "is_canary", isCanary == true)
		
		c.Next(ctx)
	}
}
