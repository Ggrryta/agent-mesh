# Redis Pub/Sub 配置热更新方案

**版本**: v1.0
**日期**: 2026-04-15
**状态**: 设计阶段

---

## 一、背景与目标

### 1.1 当前问题

- 配置修改需要重启服务
- 多实例部署时，每台机器都要改配置文件
- 运营需要频繁调整限流配置（如促销活动期间提高 QPS）
- 排查问题时需要动态切换日志级别

### 1.2 目标

- 支持限流配置（`rate_limit.*`）热更新
- 支持日志级别（`log.level`）热更新
- 多实例部署时，一次推送全部生效
- 配置变更可追溯、可回滚

---

## 二、整体架构

```
┌─────────────────────────────────────────────────────────────┐
│                    配置管理后台（Admin API）                   │
│  ┌─────────────┐  ┌──────────────┐  ┌────────────────────┐  │
│  │ 配置编辑页面 │  │ 版本历史/回滚 │  │ 权限控制/审计日志  │  │
│  └──────┬──────┘  └──────────────┘  └────────────────────┘  │
└─────────┼───────────────────────────────────────────────────┘
          │
          │ 1. 更新 MySQL
          │ 2. Redis PUBLISH config:update
          ▼
┌─────────────────────────────────────────────────────────────┐
│                         MySQL                                │
│  ┌───────────────────────────────────────────────────────┐  │
│  │ config_versions 表                                     │  │
│  │ - id, version, config_json, created_at, created_by    │  │
│  └───────────────────────────────────────────────────────┘  │
└─────────────────────────────────────────────────────────────┘
          │
          │ Redis Pub/Sub
          ▼
┌─────────────────────────────────────────────────────────────┐
│                       Redis                                  │
│  Channel: config:update                                     │
│  Message: {"version": "v20260415001", "updated_at": ...}   │
└─────────────────────────────────────────────────────────────┘
          │
    ┌─────┼─────┐
    ▼     ▼     ▼
┌───────┐┌───────┐┌───────┐
│实例 1  ││实例 2  ││实例 3  │
│收到    ││收到    ││收到    │
│reload  ││reload  ││reload  │
└───────┘└───────┘└───────┘
```

---

## 三、热更新范围

### 3.1 支持热更新的配置

| 配置项 | 优先级 | 说明 |
|--------|--------|------|
| `rate_limit.default_qps` | P0 | 全局默认 QPS |
| `rate_limit.enabled` | P0 | 是否启用限流 |
| `rate_limit.capability` | P0 | 按 capability_id 单独配置 QPS |
| `rate_limit.consumer` | P0 | 按 consumer_app_id 单独配置 QPS |
| `log.level` | P1 | 日志级别（debug/info/warn/error） |

### 3.2 不支持热更新的配置

| 配置项 | 原因 |
|--------|------|
| `server.*` | 需要重启监听端口 |
| `database.*` | 连接池已创建 |
| `redis.*` | 连接池已创建 |
| `kafka.*` | consumer/producer 已启动 |
| `jwt.secret` | 涉及安全，建议重启 |
| `telemetry.*` | tracer 已初始化 |

---

## 四、数据库设计

### 4.1 config_versions 表

```sql
CREATE TABLE config_versions (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    version VARCHAR(64) NOT NULL UNIQUE COMMENT '版本号，格式 vYYYYMMDDNNN',
    config_type VARCHAR(32) NOT NULL COMMENT '配置类型：rate_limit / log',
    config_json JSON NOT NULL COMMENT '配置内容（JSON）',
    change_summary VARCHAR(512) COMMENT '变更说明',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    created_by VARCHAR(128) COMMENT '操作人',
    INDEX idx_config_type (config_type),
    INDEX idx_created_at (created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='配置版本表';
```

### 4.2 示例数据

```sql
INSERT INTO config_versions (version, config_type, config_json, change_summary, created_by)
VALUES (
    'v20260415001',
    'rate_limit',
    '{"default_qps": 200, "enabled": true, "capability": {"skill_001": 50}, "consumer": {"app_001": 300}}',
    '促销活动期间提高全局 QPS 上限',
    'admin'
);
```

---

## 五、代码改动

### 5.1 新建配置模型和 Repo

**文件**: `internal/model/config.go`

```go
package model

import "time"

type ConfigType string

const (
    ConfigTypeRateLimit ConfigType = "rate_limit"
    ConfigTypeLog       ConfigType = "log"
)

type ConfigVersion struct {
    ID            int64       `gorm:"primaryKey;autoIncrement"`
    Version       string      `gorm:"column:version;type:varchar(64);uniqueIndex;not null"`
    ConfigType    ConfigType  `gorm:"column:config_type;type:varchar(32);not null;index"`
    ConfigJSON    string      `gorm:"column:config_json;type:json;not null"`
    ChangeSummary string      `gorm:"column:change_summary;type:varchar(512)"`
    CreatedAt     time.Time   `gorm:"column:created_at;not null"`
    CreatedBy     string      `gorm:"column:created_by;type:varchar(128)"`
}

func (ConfigVersion) TableName() string {
    return "config_versions"
}
```

**文件**: `internal/repo/config.go`

```go
package repo

import (
    "context"
    "agent-gateway/internal/model"
    "gorm.io/gorm"
)

type ConfigRepo struct {
    db *gorm.DB
}

func NewConfigRepo(db *gorm.DB) *ConfigRepo {
    return &ConfigRepo{db: db}
}

// GetLatest 获取指定类型的最新配置
func (r *ConfigRepo) GetLatest(ctx context.Context, configType model.ConfigType) (*model.ConfigVersion, error) {
    var cfg model.ConfigVersion
    err := r.db.WithContext(ctx).
        Where("config_type = ?", configType).
        Order("created_at DESC").
        First(&cfg).Error
    if err != nil {
        return nil, err
    }
    return &cfg, nil
}

// Create 创建新配置版本
func (r *ConfigRepo) Create(ctx context.Context, cfg *model.ConfigVersion) error {
    return r.db.WithContext(ctx).Create(cfg).Error
}
```

### 5.2 新建配置监听器

**文件**: `config/watcher.go`

```go
package config

import (
    "context"
    "encoding/json"
    "fmt"
    "sync"

    "agent-gateway/internal/model"
    "agent-gateway/internal/repo"
    "agent-gateway/pkg/logger"

    "github.com/redis/go-redis/v9"
    "go.uber.org/zap"
)

const (
    ConfigUpdateChannel = "config:update"
)

// ConfigWatcher 监听 Redis Pub/Sub，收到通知后从 MySQL 拉取最新配置
type ConfigWatcher struct {
    rdb       *redis.Client
    configRepo *repo.ConfigRepo
    
    mu        sync.RWMutex
    callbacks []func(cfgType model.ConfigType, cfgJSON string)
    
    done      chan struct{}
}

// NewConfigWatcher 创建配置监听器
func NewConfigWatcher(rdb *redis.Client, configRepo *repo.ConfigRepo) *ConfigWatcher {
    return &ConfigWatcher{
        rdb:        rdb,
        configRepo: configRepo,
        done:       make(chan struct{}),
    }
}

// Start 启动监听
func (w *ConfigWatcher) Start(ctx context.Context) error {
    // 订阅配置更新 channel
    sub := w.rdb.Subscribe(ctx, ConfigUpdateChannel)
    ch := sub.Channel()
    
    logger.Info("config watcher started", zap.String("channel", ConfigUpdateChannel))
    
    go func() {
        for {
            select {
            case <-w.done:
                sub.Close()
                return
            case msg, ok := <-ch:
                if !ok {
                    return
                }
                w.handleMessage(ctx, msg)
            }
        }
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

func (w *ConfigWatcher) handleMessage(ctx context.Context, msg *redis.Message) {
    // 解析消息
    var notice struct {
        Version    string `json:"version"`
        ConfigType string `json:"config_type"`
    }
    if err := json.Unmarshal([]byte(msg.Payload), &notice); err != nil {
        logger.Error("config watcher: unmarshal message failed", zap.Error(err))
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
    
    // 更新 GlobalConfig
    if err := applyConfig(cfgType, cfg.ConfigJSON); err != nil {
        logger.Error("config watcher: apply config failed", 
            zap.String("config_type", notice.ConfigType),
            zap.Error(err))
        return
    }
    
    // 触发回调
    w.mu.RLock()
    callbacks := w.callbacks
    w.mu.RUnlock()
    
    for _, cb := range callbacks {
        cb(cfgType, cfg.ConfigJSON)
    }
    
    logger.Info("config watcher: config reloaded",
        zap.String("version", cfg.Version),
        zap.String("config_type", string(cfgType)))
}

// applyConfig 将配置应用到 GlobalConfig
func applyConfig(cfgType model.ConfigType, cfgJSON string) error {
    switch cfgType {
    case model.ConfigTypeRateLimit:
        var rateLimitCfg RateLimitConfig
        if err := json.Unmarshal([]byte(cfgJSON), &rateLimitCfg); err != nil {
            return fmt.Errorf("unmarshal rate_limit config failed: %w", err)
        }
        GlobalConfigMu.Lock()
        GlobalConfig.RateLimit = rateLimitCfg
        GlobalConfigMu.Unlock()
        
    case model.ConfigTypeLog:
        var logCfg LogConfig
        if err := json.Unmarshal([]byte(cfgJSON), &logCfg); err != nil {
            return fmt.Errorf("unmarshal log config failed: %w", err)
        }
        GlobalConfigMu.Lock()
        GlobalConfig.Log = logCfg
        GlobalConfigMu.Unlock()
        // 动态调整日志级别
        if err := logger.SetLevel(logCfg.Level); err != nil {
            logger.Warn("set log level failed", zap.Error(err))
        }
        
    default:
        return fmt.Errorf("unknown config type: %s", cfgType)
    }
    
    return nil
}
```

### 5.3 修改配置包

**文件**: `config/config.go` — 添加读写锁和获取方法

```go
// 在文件末尾添加

import "sync"

// GlobalConfigMu 保护 GlobalConfig 的读写锁
var GlobalConfigMu sync.RWMutex

// GetRateLimitConfig 获取当前限流配置（线程安全）
func GetRateLimitConfig() RateLimitConfig {
    GlobalConfigMu.RLock()
    defer GlobalConfigMu.RUnlock()
    return GlobalConfig.RateLimit
}

// GetLogConfig 获取当前日志配置（线程安全）
func GetLogConfig() LogConfig {
    GlobalConfigMu.RLock()
    defer GlobalConfigMu.RUnlock()
    return GlobalConfig.Log
}
```

### 5.4 修改限流中间件

**文件**: `internal/middleware/ratelimit.go` — 每次请求读最新配置

```go
// RateLimit 双维度限流中间件（capability + consumer）
// 注意：每次请求从 GlobalConfig 读取最新配置，支持热更新
func RateLimit(rdb *redis.Client) app.HandlerFunc {
    script := redis.NewScript(rateLimitLua)

    return func(ctx context.Context, c *app.RequestContext) {
        // 每次请求读取最新配置（支持热更新）
        cfg := config.GetRateLimitConfig()
        
        if !cfg.Enabled {
            c.Next(ctx)
            return
        }

        capabilityID := c.Param("capability_id")
        appID, _ := c.Get(ContextKeyAppID)
        consumerAppID, _ := appID.(string)

        now := time.Now().UnixMilli()
        window := int64(1000)

        capQPS := cfg.DefaultQPS
        if v, ok := cfg.Capability[capabilityID]; ok && v > 0 {
            capQPS = v
        }
        // ... 后续逻辑不变
    }
}
```

### 5.5 修改日志包

**文件**: `pkg/logger/logger.go` — 支持动态调整级别

```go
// 在文件末尾添加

var (
    coreLevel zap.AtomicLevel // 原子级别，支持动态修改
)

// Init 修改为使用 AtomicLevel
func Init(level string, format string) error {
    var logLevel zapcore.Level
    switch level {
    case "debug":
        logLevel = zapcore.DebugLevel
    case "info":
        logLevel = zapcore.InfoLevel
    case "warn":
        logLevel = zapcore.WarnLevel
    case "error":
        logLevel = zapcore.ErrorLevel
    default:
        logLevel = zapcore.InfoLevel
    }
    
    coreLevel = zap.NewAtomicLevelAt(logLevel) // 原子级别

    encoderConfig := zapcore.EncoderConfig{
        // ... 保持不变
    }

    var encoder zapcore.Encoder
    if format == "console" {
        encoder = zapcore.NewConsoleEncoder(encoderConfig)
    } else {
        encoder = zapcore.NewJSONEncoder(encoderConfig)
    }

    core := zapcore.NewCore(
        encoder,
        zapcore.AddSync(os.Stdout),
        coreLevel,  // 使用原子级别
    )

    Logger = zap.New(core, zap.AddCaller(), zap.AddStacktrace(zapcore.ErrorLevel))
    return nil
}

// SetLevel 动态设置日志级别（热更新使用）
func SetLevel(level string) error {
    var logLevel zapcore.Level
    switch level {
    case "debug":
        logLevel = zapcore.DebugLevel
    case "info":
        logLevel = zapcore.InfoLevel
    case "warn":
        logLevel = zapcore.WarnLevel
    case "error":
        logLevel = zapcore.ErrorLevel
    default:
        return fmt.Errorf("unknown log level: %s", level)
    }
    
    coreLevel.SetLevel(logLevel)
    return nil
}
```

### 5.6 修改 main.go 启动监听

**文件**: `cmd/main.go` — 启动 ConfigWatcher

```go
// 在初始化 Redis 之后添加

// 5.5 初始化配置监听器（热更新）
configRepo := repo.NewConfigRepo(db)
configWatcher := config.NewConfigWatcher(rdb, configRepo)

// 注册回调：限流配置变更时打日志
configWatcher.OnConfigChange(func(cfgType model.ConfigType, cfgJSON string) {
    if cfgType == model.ConfigTypeRateLimit {
        logger.Info("rate_limit config reloaded", zap.String("config", cfgJSON))
    }
})

// 启动监听
if err := configWatcher.Start(context.Background()); err != nil {
    logger.Fatal("start config watcher failed", zap.Error(err))
}
defer configWatcher.Stop()
```

---

## 六、管理后台 API

### 6.1 更新配置 API

```
POST /admin/config
Content-Type: application/json
Authorization: Bearer {admin_token}

{
    "config_type": "rate_limit",
    "config": {
        "default_qps": 200,
        "enabled": true,
        "capability": {
            "skill_001": 50,
            "skill_002": 100
        },
        "consumer": {
            "app_001": 300
        }
    },
    "change_summary": "促销活动期间提高 QPS",
    "created_by": "admin"
}
```

**响应**:
```json
{
    "code": 0,
    "message": "success",
    "data": {
        "version": "v20260415001"
    }
}
```

### 6.2 查询配置历史 API

```
GET /admin/config/history?config_type=rate_limit&limit=10
```

### 6.3 回滚配置 API

```
POST /admin/config/rollback
{
    "version": "v20260414001"
}
```

---

## 七、测试计划

### 7.1 单元测试

| 测试项 | 说明 |
|--------|------|
| ConfigWatcher 消息解析 | 验证 JSON 反序列化 |
| applyConfig 逻辑 | 验证配置应用到 GlobalConfig |
| GetRateLimitConfig 并发 | 多 goroutine 同时读取配置 |

### 7.2 集成测试

| 测试项 | 步骤 |
|--------|------|
| 限流配置热更新 | 1. 调用 Admin API 更新配置<br>2. 验证所有实例日志打印 reload<br>3. 验证新限流配置生效 |
| 日志级别热更新 | 1. 动态切换 log.level 为 debug<br>2. 验证 debug 日志输出 |
| 离线实例上线 | 1. 实例重启<br>2. 验证启动后加载最新配置 |

### 7.3 压力测试

| 测试项 | 说明 |
|--------|------|
| 配置更新风暴 | 短时间内多次更新配置，验证服务稳定性 |
| 订阅断线重连 | 模拟 Redis 连接断开，验证自动重连 |

---

## 八、部署步骤

### 8.1 数据库迁移

```sql
-- 执行建表 SQL
source migrations/001_config_versions.sql
```

### 8.2 配置文件更新

`config/config.yaml` 新增：

```yaml
config_hot_reload:
  enabled: true
  redis_channel: "config:update"
```

### 8.3 灰度发布

1. 先升级 1 台实例，观察日志
2. 验证热更新功能正常
3. 全量发布所有实例

---

## 九、监控告警

### 9.1 监控指标

| 指标 | 说明 |
|------|------|
| `config_reload_total` | 配置重载次数 |
| `config_reload_errors` | 配置重载失败次数 |
| `config_version` | 当前配置版本（Gauge） |

### 9.2 告警规则

```yaml
# Prometheus 告警规则
- alert: ConfigReloadFailed
  expr: rate(config_reload_errors[5m]) > 0
  for: 1m
  labels:
    severity: warning
  annotations:
    summary: "配置热更新失败"
```

---

## 十、回滚方案

### 10.1 功能回滚

如果热更新功能出现问题：
1. 关闭 `config_hot_reload.enabled: false`
2. 重启服务，恢复静态配置加载
3. 后续修改配置需重启

### 10.2 配置回滚

通过 Admin API 调用 `/admin/config/rollback` 回滚到历史版本。

---

## 十一、时间规划

| 阶段 | 任务 | 预计时间 |
|------|------|---------|
| Phase 1 | 数据库迁移 + 模型定义 | 0.5 天 |
| Phase 2 | ConfigWatcher 实现 | 1 天 |
| Phase 3 | 中间件/日志改造 | 0.5 天 |
| Phase 4 | Admin API 实现 | 1 天 |
| Phase 5 | 单元测试 + 集成测试 | 1 天 |
| Phase 6 | 灰度发布 + 监控配置 | 0.5 天 |
| **总计** | | **4.5 天** |

---

## 十二、附录

### A. 消息格式

Redis Pub/Sub 消息格式：

```json
{
    "version": "v20260415001",
    "config_type": "rate_limit",
    "updated_at": "2026-04-15T10:30:00Z"
}
```

### B. 配置 JSON 示例

**rate_limit 配置**:
```json
{
    "default_qps": 200,
    "enabled": true,
    "capability": {
        "skill_001": 50,
        "skill_002": 100
    },
    "consumer": {
        "app_001": 300,
        "app_002": 150
    }
}
```

**log 配置**:
```json
{
    "level": "debug",
    "format": "json"
}
```
