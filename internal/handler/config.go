// Package handler HTTP 处理器
package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"agent-gateway/config"
	"agent-gateway/internal/model"
	"agent-gateway/internal/repo"
	"agent-gateway/pkg/ctxkey"
	"agent-gateway/pkg/logger"
	"agent-gateway/pkg/resp"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

var allConfigTypes = map[model.ConfigType]bool{
	model.ConfigTypeRateLimit:      true,
	model.ConfigTypeLog:            true,
	model.ConfigTypeTimeout:        true,
	model.ConfigTypeConcurrency:    true,
	model.ConfigTypeCircuitBreaker: true,
}

// ConfigHandler 配置管理 Handler
type ConfigHandler struct {
	configRepo   *repo.ConfigRepo
	rdb          *redis.Client
	nacosWatcher *config.NacosConfigWatcher // nil 时走 Redis Pub/Sub 模式
}

// NewConfigHandler 创建配置管理 Handler（Redis Pub/Sub 模式）
func NewConfigHandler(configRepo *repo.ConfigRepo, rdb *redis.Client) *ConfigHandler {
	return &ConfigHandler{configRepo: configRepo, rdb: rdb}
}

// NewConfigHandlerWithNacos 创建配置管理 Handler（Nacos 模式）
func NewConfigHandlerWithNacos(configRepo *repo.ConfigRepo, rdb *redis.Client, nacosWatcher *config.NacosConfigWatcher) *ConfigHandler {
	return &ConfigHandler{configRepo: configRepo, rdb: rdb, nacosWatcher: nacosWatcher}
}

// UpdateConfigRequest 更新配置请求
type UpdateConfigRequest struct {
	ConfigType    string          `json:"config_type" binding:"required"`
	Config        json.RawMessage `json:"config" binding:"required"`
	ChangeSummary string          `json:"change_summary"`
	CreatedBy     string          `json:"created_by"`
}

// UpdateConfigResponse 更新配置响应
type UpdateConfigResponse struct {
	Version string `json:"version"`
}

// UpdateConfig POST /admin/config
func (h *ConfigHandler) UpdateConfig(c context.Context, ctx *app.RequestContext) {
	var req UpdateConfigRequest
	if err := ctx.BindJSON(&req); err != nil {
		ctx.JSON(consts.StatusBadRequest, resp.Err(resp.CodeBadRequest, "invalid request: "+err.Error()))
		return
	}

	cfgType := model.ConfigType(req.ConfigType)
	if !allConfigTypes[cfgType] {
		ctx.JSON(consts.StatusBadRequest, resp.Err(resp.CodeBadRequest, "invalid config_type: "+req.ConfigType))
		return
	}

	if err := validateConfigJSON(cfgType, req.Config); err != nil {
		ctx.JSON(consts.StatusBadRequest, resp.Err(resp.CodeBadRequest, err.Error()))
		return
	}

	version := newVersion()
	createdBy := ctx.GetString(ctxkey.AppID)
	if createdBy == "" {
		createdBy = req.CreatedBy
	}

	cv := &model.ConfigVersion{
		Version:       version,
		ConfigType:    cfgType,
		ConfigJSON:    string(req.Config),
		ChangeSummary: req.ChangeSummary,
		CreatedAt:     time.Now(),
		CreatedBy:     createdBy,
	}
	if err := h.configRepo.Create(c, cv); err != nil {
		logger.Error("create config version failed", zap.Error(err))
		ctx.JSON(consts.StatusInternalServerError, resp.Err(resp.CodeInternalServerError, "create config version failed"))
		return
	}

	// 发布到配置中心
	if h.nacosWatcher != nil {
		if err := h.nacosWatcher.PublishConfig(cfgType, string(req.Config)); err != nil {
			logger.Error("publish config to nacos failed", zap.Error(err))
			// Nacos 发布失败不阻断：配置已落 MySQL，watcher 轮询会兜底
		}
	} else {
		if err := config.PublishConfigUpdate(c, h.rdb, version, cfgType); err != nil {
			logger.Error("publish config update failed", zap.Error(err))
		}
	}

	logger.Info("config updated",
		zap.String("version", version),
		zap.String("config_type", string(cfgType)),
		zap.String("created_by", createdBy))

	ctx.JSON(consts.StatusOK, resp.OK(UpdateConfigResponse{Version: version}))
}

// ConfigHistoryResponse 配置历史响应
type ConfigHistoryResponse struct {
	List  []model.ConfigVersion `json:"list"`
	Total int64                 `json:"total"`
}

// GetConfigHistory GET /admin/config/history?config_type=rate_limit&limit=10&offset=0
func (h *ConfigHandler) GetConfigHistory(c context.Context, ctx *app.RequestContext) {
	cfgTypeStr := string(ctx.Query("config_type"))
	if cfgTypeStr == "" {
		ctx.JSON(consts.StatusBadRequest, resp.Err(resp.CodeBadRequest, "config_type is required"))
		return
	}

	cfgType := model.ConfigType(cfgTypeStr)
	if !allConfigTypes[cfgType] {
		ctx.JSON(consts.StatusBadRequest, resp.Err(resp.CodeBadRequest, "invalid config_type: "+cfgTypeStr))
		return
	}

	limit := 10
	offset := 0

	list, total, err := h.configRepo.ListByType(c, cfgType, limit, offset)
	if err != nil {
		logger.Error("list config history failed", zap.Error(err))
		ctx.JSON(consts.StatusInternalServerError, resp.Err(resp.CodeInternalServerError, "list config history failed"))
		return
	}

	ctx.JSON(consts.StatusOK, resp.OK(ConfigHistoryResponse{List: list, Total: total}))
}

// RollbackConfigRequest 回滚配置请求
type RollbackConfigRequest struct {
	Version string `json:"version" binding:"required"`
}

// RollbackConfig POST /admin/config/rollback
func (h *ConfigHandler) RollbackConfig(c context.Context, ctx *app.RequestContext) {
	var req RollbackConfigRequest
	if err := ctx.BindJSON(&req); err != nil {
		ctx.JSON(consts.StatusBadRequest, resp.Err(resp.CodeBadRequest, "invalid request: "+err.Error()))
		return
	}

	cv, err := h.configRepo.GetByVersion(c, req.Version)
	if err != nil {
		ctx.JSON(consts.StatusNotFound, resp.Err(resp.CodeNotFound, "config version not found"))
		return
	}

	newVer := newVersion()
	newCV := &model.ConfigVersion{
		Version:       newVer,
		ConfigType:    cv.ConfigType,
		ConfigJSON:    cv.ConfigJSON,
		ChangeSummary: fmt.Sprintf("rollback to %s", req.Version),
		CreatedAt:     time.Now(),
		CreatedBy:     ctx.GetString(ctxkey.AppID),
	}
	if err := h.configRepo.Create(c, newCV); err != nil {
		logger.Error("create rollback config version failed", zap.Error(err))
		ctx.JSON(consts.StatusInternalServerError, resp.Err(resp.CodeInternalServerError, "create rollback config version failed"))
		return
	}

	if h.nacosWatcher != nil {
		if err := h.nacosWatcher.PublishConfig(cv.ConfigType, cv.ConfigJSON); err != nil {
			logger.Error("publish rollback config to nacos failed", zap.Error(err))
		}
	} else {
		if err := config.PublishConfigUpdate(c, h.rdb, newVer, cv.ConfigType); err != nil {
			logger.Error("publish config update failed", zap.Error(err))
		}
	}

	logger.Info("config rolled back",
		zap.String("new_version", newVer),
		zap.String("from_version", req.Version))

	ctx.JSON(consts.StatusOK, resp.OK(UpdateConfigResponse{Version: newVer}))
}

// newVersion 生成无碰撞版本号：v{date}-{uuid前8位}
func newVersion() string {
	return fmt.Sprintf("v%s-%s", time.Now().Format("20060102"), uuid.New().String()[:8])
}

// validateConfigJSON 校验配置内容格式
func validateConfigJSON(cfgType model.ConfigType, raw json.RawMessage) error {
	switch cfgType {
	case model.ConfigTypeRateLimit:
		var v model.RateLimitConfigJSON
		if err := json.Unmarshal(raw, &v); err != nil {
			return fmt.Errorf("invalid rate_limit config: %w", err)
		}
	case model.ConfigTypeLog:
		var v model.LogConfigJSON
		if err := json.Unmarshal(raw, &v); err != nil {
			return fmt.Errorf("invalid log config: %w", err)
		}
	case model.ConfigTypeTimeout:
		var v model.TimeoutConfigJSON
		if err := json.Unmarshal(raw, &v); err != nil {
			return fmt.Errorf("invalid timeout config: %w", err)
		}
	case model.ConfigTypeConcurrency:
		var v model.ConcurrencyConfigJSON
		if err := json.Unmarshal(raw, &v); err != nil {
			return fmt.Errorf("invalid concurrency config: %w", err)
		}
	case model.ConfigTypeCircuitBreaker:
		var v model.CircuitBreakerConfigJSON
		if err := json.Unmarshal(raw, &v); err != nil {
			return fmt.Errorf("invalid circuit_breaker config: %w", err)
		}
	}
	return nil
}
