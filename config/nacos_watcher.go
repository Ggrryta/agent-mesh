package config

import (
	"fmt"
	"sync"

	"agent-gateway/internal/model"
	"agent-gateway/pkg/logger"

	"go.uber.org/zap"
)

// NacosConfigWatcher 基于 Nacos 的配置监听器
// 支持动态扩展、灰度发布、多环境隔离
type NacosConfigWatcher struct {
	nacosClient *NacosClient
	group       string
	canaryCfg   *CanaryConfig // 灰度配置

	mu        sync.RWMutex
	callbacks []func(cfgType model.ConfigType, cfgJSON string)

	// 版本追踪（避免重复应用）
	lastVersions map[model.ConfigType]string
}

// NewNacosConfigWatcher 创建 Nacos 配置监听器
func NewNacosConfigWatcher(nacosClient *NacosClient, group string, canaryCfg *CanaryConfig) *NacosConfigWatcher {
	return &NacosConfigWatcher{
		nacosClient:  nacosClient,
		group:        group,
		canaryCfg:    canaryCfg,
		lastVersions: make(map[model.ConfigType]string),
	}
}

// OnConfigChange 注册配置变更回调
func (w *NacosConfigWatcher) OnConfigChange(cb func(cfgType model.ConfigType, cfgJSON string)) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.callbacks = append(w.callbacks, cb)
}

// Start 启动所有配置类型的监听
func (w *NacosConfigWatcher) Start() error {
	if w.nacosClient == nil {
		logger.Info("nacos client is nil, config watcher not started")
		return nil
	}

	// 所有需要监听的配置类型
	configTypes := []model.ConfigType{
		model.ConfigTypeRateLimit,
		model.ConfigTypeLog,
		model.ConfigTypeTimeout,
		model.ConfigTypeConcurrency,
		model.ConfigTypeCircuitBreaker,
	}

	// 为每种配置类型启动监听
	for _, cfgType := range configTypes {
		// 构建 DataID（支持灰度）
		baseDataID := string(cfgType) + ".json"
		dataID := w.buildCanaryDataID(baseDataID)

		// 先拉取初始配置
		content, err := w.nacosClient.GetConfig(dataID, w.group)
		if err != nil {
			logger.Warn("nacos watcher: get initial config failed",
				zap.String("data_id", dataID),
				zap.Error(err))
			continue
		}

		// 应用初始配置
		if err := w.applyConfig(cfgType, content); err != nil {
			logger.Error("nacos watcher: apply initial config failed",
				zap.String("data_id", dataID),
				zap.Error(err))
			continue
		}

		// 启动监听
		if err := w.startListener(cfgType, dataID); err != nil {
			logger.Error("nacos watcher: start listener failed",
				zap.String("data_id", dataID),
				zap.Error(err))
			return err
		}
	}

	logger.Info("nacos config watcher started",
		zap.String("group", w.group),
		zap.Int("config_types", len(configTypes)),
		zap.Bool("canary", w.isCanaryMode()))

	return nil
}

// buildCanaryDataID 构建灰度 DataID
func (w *NacosConfigWatcher) buildCanaryDataID(baseDataID string) string {
	if w.canaryCfg != nil {
		return w.canaryCfg.BuildCanaryDataID(baseDataID)
	}
	return baseDataID
}

// isCanaryMode 是否为灰度模式
func (w *NacosConfigWatcher) isCanaryMode() bool {
	return w.canaryCfg != nil && w.canaryCfg.IsCanaryInstance()
}

// startListener 启动单个配置类型的监听
func (w *NacosConfigWatcher) startListener(cfgType model.ConfigType, dataID string) error {
	return w.nacosClient.ListenConfig(dataID, w.group, func(namespace, group, dataId, data string) {
		logger.Info("nacos watcher: config changed",
			zap.String("data_id", dataId),
			zap.String("group", group))

		// 提取配置类型（从 dataId 去掉 .json 后缀）
		cfgType := model.ConfigType(dataId[:len(dataId)-5])

		// 应用新配置
		if err := w.applyConfig(cfgType, data); err != nil {
			logger.Error("nacos watcher: apply config failed",
				zap.String("data_id", dataId),
				zap.Error(err))
			return
		}

		// 触发回调
		w.mu.RLock()
		callbacks := w.callbacks
		w.mu.RUnlock()

		for _, cb := range callbacks {
			cb(cfgType, data)
		}
	})
}

// applyConfig 将配置应用到 GlobalConfig（委托给包级函数）
func (w *NacosConfigWatcher) applyConfig(cfgType model.ConfigType, cfgJSON string) error {
	return applyConfig(cfgType, cfgJSON)
}

// PublishConfig 发布配置更新（供 Admin API 调用）
func (w *NacosConfigWatcher) PublishConfig(cfgType model.ConfigType, cfgJSON string) error {
	dataID := string(cfgType) + ".json"
	ok, err := w.nacosClient.PublishConfig(dataID, w.group, cfgJSON)
	if err != nil {
		return fmt.Errorf("publish config failed: %w", err)
	}
	if !ok {
		return fmt.Errorf("publish config returned false")
	}

	logger.Info("nacos watcher: config published",
		zap.String("config_type", string(cfgType)),
		zap.String("data_id", dataID))

	return nil
}

// Stop 停止监听
func (w *NacosConfigWatcher) Stop() {
	if w.nacosClient != nil {
		w.nacosClient.Close()
	}
	logger.Info("nacos config watcher stopped")
}
