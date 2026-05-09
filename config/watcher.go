// Package config 配置管理
package config

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"agent-gateway/internal/model"
	"agent-gateway/internal/repo"
	"agent-gateway/pkg/logger"

	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

const (
	// ConfigUpdateChannel Redis Pub/Sub 频道名称
	ConfigUpdateChannel = "config:update"
)

// ConfigUpdateNotice 配置更新通知消息结构
type ConfigUpdateNotice struct {
	Version    string `json:"version"`
	ConfigType string `json:"config_type"`
	UpdatedAt  string `json:"updated_at"`
}

// ConfigWatcher 监听 Redis Pub/Sub，收到通知后从 MySQL 拉取最新配置
// 同时定时从 MySQL 拉取兜底（防止 Redis 断线丢失通知）
type ConfigWatcher struct {
	rdb        *redis.Client
	configRepo *repo.ConfigRepo

	mu        sync.RWMutex
	callbacks []func(cfgType model.ConfigType, cfgJSON string)

	// 定时拉取
	pollInterval time.Duration
	// 版本追踪（避免重复应用）
	lastVersions map[model.ConfigType]string

	// 回调并发控制
	callbackSem chan struct{} // 限制同时执行的回调 goroutine 数量
	
	done chan struct{}
}

// NewConfigWatcher 创建配置监听器
func NewConfigWatcher(rdb *redis.Client, configRepo *repo.ConfigRepo) *ConfigWatcher {
	return &ConfigWatcher{
		rdb:          rdb,
		configRepo:   configRepo,
		pollInterval: 30 * time.Second, // 默认 30 秒拉一次
		lastVersions: make(map[model.ConfigType]string),
		callbackSem:  make(chan struct{}, 10), // 最多 10 个回调并发执行
		done:         make(chan struct{}),
	}
}

// Start 启动监听
func (w *ConfigWatcher) Start(ctx context.Context) error {
	// 订阅配置更新 channel
	sub := w.rdb.Subscribe(ctx, ConfigUpdateChannel)
	_, err := sub.Receive(ctx)
	if err != nil {
		return fmt.Errorf("subscribe config channel failed: %w", err)
	}

	ch := sub.Channel()

	logger.Info("config watcher started",
		zap.String("channel", ConfigUpdateChannel),
		zap.String("redis_addr", w.rdb.Options().Addr),
		zap.Duration("poll_interval", w.pollInterval))

	// 启动 Pub/Sub 监听
	go func() {
		defer func() {
			if r := recover(); r != nil {
				logger.Error("config watcher goroutine panic", zap.Any("recover", r))
			}
		}()
		
		for {
			select {
			case <-w.done:
				sub.Close()
				logger.Info("config watcher stopped")
				return
			case msg, ok := <-ch:
				if !ok {
					logger.Warn("config watcher channel closed, attempting reconnect")
					time.Sleep(time.Second)
					sub = w.rdb.Subscribe(context.Background(), ConfigUpdateChannel)
					ch = sub.Channel()
					continue
				}
				w.handleMessage(ctx, msg)
			}
		}
	}()

	// 启动定时拉取兜底
	go func() {
		defer func() {
			if r := recover(); r != nil {
				logger.Error("config watcher pollLoop goroutine panic", zap.Any("recover", r))
			}
		}()
		w.pollLoop(ctx)
	}()

	return nil
}

// Stop 停止监听
func (w *ConfigWatcher) Stop() {
	close(w.done)
}

// OnConfigChange 注册配置变更回调
func (w *ConfigWatcher) OnConfigChange(callback func(cfgType model.ConfigType, cfgJSON string)) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.callbacks = append(w.callbacks, callback)
}

// handleMessage 处理 Redis Pub/Sub 消息
func (w *ConfigWatcher) handleMessage(ctx context.Context, msg *redis.Message) {
	// 解析消息
	var notice ConfigUpdateNotice
	if err := json.Unmarshal([]byte(msg.Payload), &notice); err != nil {
		logger.Error("config watcher: unmarshal message failed",
			zap.String("payload", msg.Payload),
			zap.Error(err))
		return
	}

	logger.Info("config watcher: received update notice",
		zap.String("version", notice.Version),
		zap.String("config_type", notice.ConfigType))

	// 从 MySQL 拉取最新配置
	cfgType := model.ConfigType(notice.ConfigType)
	cfg, err := w.configRepo.GetLatest(ctx, cfgType)
	if err != nil {
		logger.Error("config watcher: get latest config failed",
			zap.String("config_type", notice.ConfigType),
			zap.Error(err))
		return
	}

	// 应用配置到 GlobalConfig
	if err := applyConfig(cfgType, cfg.ConfigJSON); err != nil {
		logger.Error("config watcher: apply config failed",
			zap.String("config_type", notice.ConfigType),
			zap.Error(err))
		return
	}

	// 更新版本追踪
	w.mu.Lock()
	w.lastVersions[cfgType] = cfg.Version
	w.mu.Unlock()

	// 触发回调（异步执行，避免阻塞 Pub/Sub 监听）
	w.mu.RLock()
	callbacks := w.callbacks
	w.mu.RUnlock()

	// 使用信号量限制并发回调数量
	select {
	case w.callbackSem <- struct{}{}:
		// 获取信号量，执行回调
		go func() {
			defer func() {
				<-w.callbackSem // 释放信号量
				if r := recover(); r != nil {
					logger.Error("config watcher callback panic",
						zap.String("config_type", string(cfgType)),
						zap.Any("recover", r))
				}
			}()
			
			for _, cb := range callbacks {
				cb(cfgType, cfg.ConfigJSON)
			}
		}()
	default:
		// 信号量满，跳过本次回调（防止 goroutine 泄漏）
		logger.Warn("config watcher: callback semaphore full, skipping",
			zap.String("config_type", string(cfgType)))
	}

	logger.Info("config watcher: config reloaded",
		zap.String("version", cfg.Version),
		zap.String("config_type", string(cfgType)))
}

// applyConfig 将配置应用到 GlobalConfig（copy-on-write + atomic.Value）
func applyConfig(cfgType model.ConfigType, cfgJSON string) error {
	cfg := GetGlobalConfig()

	switch cfgType {
	case model.ConfigTypeRateLimit:
		var rateLimitCfg RateLimitConfig
		if err := json.Unmarshal([]byte(cfgJSON), &rateLimitCfg); err != nil {
			return fmt.Errorf("unmarshal rate_limit config failed: %w", err)
		}
		cfg.RateLimit = rateLimitCfg

	case model.ConfigTypeLog:
		var logCfg LogConfig
		if err := json.Unmarshal([]byte(cfgJSON), &logCfg); err != nil {
			return fmt.Errorf("unmarshal log config failed: %w", err)
		}
		cfg.Log = logCfg
		storeGlobalConfig(cfg)
		if err := logger.SetLevel(logCfg.Level); err != nil {
			logger.Warn("set log level failed", zap.Error(err))
		}
		return nil

	case model.ConfigTypeTimeout:
		var timeoutCfg model.TimeoutConfigJSON
		if err := json.Unmarshal([]byte(cfgJSON), &timeoutCfg); err != nil {
			return fmt.Errorf("unmarshal timeout config failed: %w", err)
		}
		if cfg.Timeout == nil {
			cfg.Timeout = &TimeoutConfig{}
		}
		cfg.Timeout.GlobalMs = timeoutCfg.Global
		cfg.Timeout.RedisMs = timeoutCfg.Redis
		cfg.Timeout.QueueMs = timeoutCfg.Queue
		cfg.Timeout.DownstreamMs = timeoutCfg.Downstream

	case model.ConfigTypeConcurrency:
		var concCfg model.ConcurrencyConfigJSON
		if err := json.Unmarshal([]byte(cfgJSON), &concCfg); err != nil {
			return fmt.Errorf("unmarshal concurrency config failed: %w", err)
		}
		if cfg.Concurrency == nil {
			cfg.Concurrency = &ConcurrencyConfig{}
		}
		cfg.Concurrency.MaxConcurrency = concCfg.MaxConcurrency
		cfg.Concurrency.FailureThreshold = concCfg.FailureThreshold
		cfg.Concurrency.RecoveryTimeoutS = concCfg.RecoveryTimeoutS

	case model.ConfigTypeCircuitBreaker:
		var cbCfg model.CircuitBreakerConfigJSON
		if err := json.Unmarshal([]byte(cfgJSON), &cbCfg); err != nil {
			return fmt.Errorf("unmarshal circuit_breaker config failed: %w", err)
		}
		if cfg.CircuitBreaker == nil {
			cfg.CircuitBreaker = &CircuitBreakerConfig{}
		}
		cfg.CircuitBreaker.ErrorRateThreshold = cbCfg.ErrorRateThreshold
		cfg.CircuitBreaker.MinRequests = cbCfg.MinRequests
		cfg.CircuitBreaker.RecoveryIntervalS = cbCfg.RecoveryIntervalS
		cfg.CircuitBreaker.MaxRequests = cbCfg.MaxRequests

	default:
		return fmt.Errorf("unknown config type: %s", cfgType)
	}

	storeGlobalConfig(cfg)
	return nil
}

// PublishConfigUpdate 发布配置更新通知（供 Admin API 调用）
func PublishConfigUpdate(ctx context.Context, rdb *redis.Client, version string, cfgType model.ConfigType) error {
	notice := ConfigUpdateNotice{
		Version:    version,
		ConfigType: string(cfgType),
		UpdatedAt:  time.Now().Format(time.RFC3339),
	}

	payload, err := json.Marshal(notice)
	if err != nil {
		return fmt.Errorf("marshal config update notice failed: %w", err)
	}

	if err := rdb.Publish(ctx, ConfigUpdateChannel, payload).Err(); err != nil {
		return fmt.Errorf("publish config update failed: %w", err)
	}

	logger.Info("config update published",
		zap.String("version", version),
		zap.String("config_type", string(cfgType)))

	return nil
}

// pollLoop 定时从 MySQL 拉取配置（兜底机制）
// 防止 Redis 断线期间丢失通知
func (w *ConfigWatcher) pollLoop(ctx context.Context) {
	ticker := time.NewTicker(w.pollInterval)
	defer ticker.Stop()

	// 启动时立即拉一次
	w.pollAllTypes(ctx)

	for {
		select {
		case <-w.done:
			return
		case <-ticker.C:
			w.pollAllTypes(ctx)
		}
	}
}

// pollAllTypes 拉取所有配置类型的最新版本
func (w *ConfigWatcher) pollAllTypes(ctx context.Context) {
	allTypes := []model.ConfigType{
		model.ConfigTypeRateLimit,
		model.ConfigTypeLog,
		model.ConfigTypeTimeout,
		model.ConfigTypeConcurrency,
		model.ConfigTypeCircuitBreaker,
	}

	for _, cfgType := range allTypes {
		cfg, err := w.configRepo.GetLatest(ctx, cfgType)
		if err != nil {
			continue
		}

		// 版本未变化，跳过
		if cfg.Version == w.lastVersions[cfgType] {
			continue
		}

		// 应用配置
		if err := applyConfig(cfgType, cfg.ConfigJSON); err != nil {
			logger.Error("config poll: apply config failed",
				zap.String("config_type", string(cfgType)),
				zap.Error(err))
			continue
		}

		// 更新版本追踪
		w.mu.Lock()
		w.lastVersions[cfgType] = cfg.Version
		w.mu.Unlock()

		// 触发回调
		w.mu.RLock()
		callbacks := w.callbacks
		w.mu.RUnlock()
		for _, cb := range callbacks {
			cb(cfgType, cfg.ConfigJSON)
		}

		logger.Info("config poll: config reloaded",
			zap.String("version", cfg.Version),
			zap.String("config_type", string(cfgType)))
	}
}
