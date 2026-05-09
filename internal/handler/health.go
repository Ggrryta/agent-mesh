package handler

import (
	"context"
	"time"

	"agent-gateway/pkg/logger"
	"agent-gateway/pkg/resp"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// HealthCheck 深度健康检查
type HealthCheck struct {
	db          *gorm.DB
	rdb         *redis.Client
	nacosClient interface{} // 可选
}

// NewHealthCheck 创建健康检查
func NewHealthCheck(db *gorm.DB, rdb *redis.Client) *HealthCheck {
	return &HealthCheck{
		db:  db,
		rdb: rdb,
	}
}

// HealthResult 健康检查结果
type HealthResult struct {
	Status    string            `json:"status"`    // ok / degraded / unhealthy
	Timestamp string            `json:"timestamp"`
	Checks    map[string]Check `json:"checks"`
}

// Check 单项检查
type Check struct {
	Status  string `json:"status"`  // ok / error
	Latency string `json:"latency"` // 响应时间
	Error   string `json:"error,omitempty"`
}

// Check 深度健康检查 GET /health/deep
func (h *HealthCheck) Check(ctx context.Context, c *app.RequestContext) {
	result := HealthResult{
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Checks:    make(map[string]Check),
	}

	// 1. MySQL 检查
	mysqlCheck := h.checkMySQL(ctx)
	result.Checks["mysql"] = mysqlCheck

	// 2. Redis 检查
	redisCheck := h.checkRedis(ctx)
	result.Checks["redis"] = redisCheck

	// 3. 综合状态
	if mysqlCheck.Status == "ok" && redisCheck.Status == "ok" {
		result.Status = "ok"
	} else if mysqlCheck.Status == "error" || redisCheck.Status == "error" {
		result.Status = "unhealthy"
		c.JSON(consts.StatusServiceUnavailable, result)
		return
	} else {
		result.Status = "degraded"
	}

	c.JSON(consts.StatusOK, result)
}

// Ping 简单健康检查 GET /ping（兼容旧版）
func (h *HealthCheck) Ping(ctx context.Context, c *app.RequestContext) {
	c.JSON(consts.StatusOK, resp.OK(map[string]string{
		"status":    "ok",
		"timestamp": time.Now().UTC().Format(time.RFC3339),
	}))
}

func (h *HealthCheck) checkMySQL(ctx context.Context) Check {
	start := time.Now()
	
	// 使用 ping 检查连接
	sqlDB, err := h.db.DB()
	if err != nil {
		return Check{
			Status:  "error",
			Latency: "0ms",
			Error:   "failed to get sql.DB: " + err.Error(),
		}
	}

	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	if err := sqlDB.PingContext(ctx); err != nil {
		return Check{
			Status:  "error",
			Latency: time.Since(start).String(),
			Error:   "ping failed: " + err.Error(),
		}
	}

	return Check{
		Status:  "ok",
		Latency: time.Since(start).String(),
	}
}

func (h *HealthCheck) checkRedis(ctx context.Context) Check {
	start := time.Now()

	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	if err := h.rdb.Ping(ctx).Err(); err != nil {
		return Check{
			Status:  "error",
			Latency: time.Since(start).String(),
			Error:   "ping failed: " + err.Error(),
		}
	}

	return Check{
		Status:  "ok",
		Latency: time.Since(start).String(),
	}
}

// Ready 就绪检查（K8s Readiness Probe）
func (h *HealthCheck) Ready(ctx context.Context, c *app.RequestContext) {
	// 检查所有依赖是否就绪
	mysqlOK := h.isMySQLReady(ctx)
	redisOK := h.isRedisReady(ctx)

	if mysqlOK && redisOK {
		c.JSON(consts.StatusOK, resp.OK(map[string]string{"status": "ready"}))
	} else {
		logger.Ctx(ctx).Warn("not ready",
			zap.Bool("mysql", mysqlOK),
			zap.Bool("redis", redisOK),
		)
		c.JSON(consts.StatusServiceUnavailable, resp.Err(503, "not ready"))
	}
}

func (h *HealthCheck) isMySQLReady(ctx context.Context) bool {
	sqlDB, err := h.db.DB()
	if err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, 1*time.Second)
	defer cancel()
	return sqlDB.PingContext(ctx) == nil
}

func (h *HealthCheck) isRedisReady(ctx context.Context) bool {
	ctx, cancel := context.WithTimeout(ctx, 1*time.Second)
	defer cancel()
	return h.rdb.Ping(ctx).Err() == nil
}
