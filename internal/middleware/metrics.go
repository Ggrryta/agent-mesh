package middleware

import (
	"context"
	"strconv"
	"time"

	"agent-gateway/pkg/metrics"

	"github.com/cloudwego/hertz/pkg/app"
)

// Metrics records per-request Prometheus metrics (request count + latency).
// Must be placed after Tracing() so the span context is available.
func Metrics() app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		start := time.Now()
		c.Next(ctx)

		skillID := extractSkillID(c)
		status := strconv.Itoa(c.Response.StatusCode())
		elapsed := time.Since(start).Seconds()

		metrics.RequestsTotal.WithLabelValues(skillID, status).Inc()
		metrics.RequestDuration.WithLabelValues(skillID).Observe(elapsed)
	}
}

func extractSkillID(c *app.RequestContext) string {
	if id := c.Param("agent_id"); id != "" {
		return id
	}
	if id := c.Param("skill_id"); id != "" {
		return id
	}
	return "unknown"
}
