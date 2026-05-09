package config

import (
	"fmt"
	"strings"
	"sync/atomic"

	"github.com/spf13/viper"
)

// Config 全局配置
type Config struct {
	Server         ServerConfig          `mapstructure:"server"`
	Database       DatabaseConfig        `mapstructure:"database"`
	Redis          RedisConfig           `mapstructure:"redis"`
	Nacos          NacosConfig           `mapstructure:"nacos"`
	JWT            JWTConfig             `mapstructure:"jwt"`
	RateLimit      RateLimitConfig       `mapstructure:"rate_limit"`
	AsyncMQ        AsyncMQConfig         `mapstructure:"async_mq"`
	Log            LogConfig             `mapstructure:"log"`
	Telemetry      TelemetryConfig       `mapstructure:"telemetry"`
	Timeout        *TimeoutConfig        `mapstructure:"timeout"`
	Concurrency    *ConcurrencyConfig    `mapstructure:"concurrency"`
	CircuitBreaker *CircuitBreakerConfig `mapstructure:"circuit_breaker"`
	InternalToken  string                `mapstructure:"internal_token"`
}

// NacosConfig Nacos 配置中心
type NacosConfig struct {
	Enabled              bool   `mapstructure:"enabled"`                // 是否启用 Nacos
	Host                 string `mapstructure:"host"`                   // Nacos 服务器地址
	Port                 uint64 `mapstructure:"port"`                   // Nacos 端口
	Namespace            string `mapstructure:"namespace"`              // 命名空间（环境隔离）
	Group                string `mapstructure:"group"`                  // 配置分组
	DataID               string `mapstructure:"data_id"`                // 配置 ID
	Username             string `mapstructure:"username"`               // 用户名
	Password             string `mapstructure:"password"`               // 密码
	CacheDir             string `mapstructure:"cache_dir"`              // 本地缓存目录
	LogLevel             string `mapstructure:"log_level"`              // Nacos SDK 日志级别
	AgentRegistryService string `mapstructure:"agent_registry_service"` // Agent 注册服务名（默认 agent-registry）
}

// ServerConfig 服务器配置
type ServerConfig struct {
	Port int    `mapstructure:"port"`
	Mode string `mapstructure:"mode"`
}

// DatabaseConfig 数据库配置
type DatabaseConfig struct {
	Host         string `mapstructure:"host"`
	Port         int    `mapstructure:"port"`
	User         string `mapstructure:"user"`
	Password     string `mapstructure:"password"`
	DBName       string `mapstructure:"dbname"`
	Charset      string `mapstructure:"charset"`
	MaxIdleConns int    `mapstructure:"max_idle_conns"`
	MaxOpenConns int    `mapstructure:"max_open_conns"`
}

// DSN 返回 MySQL 连接字符串
func (d *DatabaseConfig) DSN() string {
	return fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?charset=%s&parseTime=True&loc=Local",
		d.User, d.Password, d.Host, d.Port, d.DBName, d.Charset)
}

// RedisConfig Redis 配置
type RedisConfig struct {
	Host     string `mapstructure:"host"`
	Port     int    `mapstructure:"port"`
	Password string `mapstructure:"password"`
	DB       int    `mapstructure:"db"`
	PoolSize int    `mapstructure:"pool_size"`
}

// Addr 返回 Redis 地址
func (r *RedisConfig) Addr() string {
	return fmt.Sprintf("%s:%d", r.Host, r.Port)
}

// JWTConfig JWT 配置
type JWTConfig struct {
	Secret      string `mapstructure:"secret"`
	ExpireHours int    `mapstructure:"expire_hours"`
}

// RateLimitConfig 限流配置
type RateLimitConfig struct {
	DefaultQPS int            `mapstructure:"default_qps"`
	Enabled    bool           `mapstructure:"enabled"`
	Capability map[string]int `mapstructure:"capability"` // agent_id → qps（yaml key 保持 capability 向后兼容）
	Consumer   map[string]int `mapstructure:"consumer"`   // consumer_app_id → qps
}

// AsyncMQConfig controls the reliable async task event queue.
type AsyncMQConfig struct {
	Type        string   `mapstructure:"type"` // memory / kafka
	Brokers     []string `mapstructure:"brokers"`
	Topic       string   `mapstructure:"topic"`
	GroupID     string   `mapstructure:"group_id"`
	QueueBuffer int      `mapstructure:"queue_buffer"`
}

// LogConfig 日志配置
type LogConfig struct {
	Level  string `mapstructure:"level"`
	Format string `mapstructure:"format"`
}

// TelemetryConfig OpenTelemetry 配置
type TelemetryConfig struct {
	ServiceName  string  `mapstructure:"service_name"`
	OTLPEndpoint string  `mapstructure:"otlp_endpoint"` // 空则禁用，如 "localhost:4317"
	SampleRate   float64 `mapstructure:"sample_rate"`   // 采样率 0.0–1.0，默认 0.1
}

// TimeoutConfig 超时配置（四层超时，毫秒单位）
type TimeoutConfig struct {
	GlobalMs     int `mapstructure:"global_ms"`
	RedisMs      int `mapstructure:"redis_ms"`
	QueueMs      int `mapstructure:"queue_ms"`
	DownstreamMs int `mapstructure:"downstream_ms"`
}

// ConcurrencyConfig 并发控制配置
type ConcurrencyConfig struct {
	MaxConcurrency   int `mapstructure:"max_concurrency"`
	FailureThreshold int `mapstructure:"failure_threshold"`
	RecoveryTimeoutS int `mapstructure:"recovery_timeout_s"`
}

// CircuitBreakerConfig 熔断器配置
type CircuitBreakerConfig struct {
	ErrorRateThreshold float64 `mapstructure:"error_rate_threshold"`
	MinRequests        int     `mapstructure:"min_requests"`
	RecoveryIntervalS  int     `mapstructure:"recovery_interval_s"`
	MaxRequests        int     `mapstructure:"max_requests"`
}

// globalConfig 全局配置实例（atomic.Value 存储 *Config，无锁读取）
var globalConfig atomic.Value

func loadGlobalConfig() *Config {
	v := globalConfig.Load()
	if v == nil {
		return nil
	}
	return v.(*Config)
}

func storeGlobalConfig(cfg *Config) {
	globalConfig.Store(cfg)
}

// GetRateLimitConfig 获取当前限流配置（线程安全，支持热更新）
func GetRateLimitConfig() RateLimitConfig {
	cfg := loadGlobalConfig()
	if cfg == nil {
		return RateLimitConfig{}
	}
	return cfg.RateLimit
}

// GetLogConfig 获取当前日志配置（线程安全，支持热更新）
func GetLogConfig() LogConfig {
	cfg := loadGlobalConfig()
	if cfg == nil {
		return LogConfig{}
	}
	return cfg.Log
}

// GetRedisConfig 获取 Redis 配置
func GetRedisConfig() RedisConfig {
	cfg := loadGlobalConfig()
	if cfg == nil {
		return RedisConfig{}
	}
	return cfg.Redis
}

// GetCircuitBreakerConfig 获取当前熔断器配置（线程安全，支持热更新）
func GetCircuitBreakerConfig() *CircuitBreakerConfig {
	cfg := loadGlobalConfig()
	if cfg == nil {
		return nil
	}
	return cfg.CircuitBreaker
}

// GetGlobalConfig 获取全局配置快照（线程安全）
func GetGlobalConfig() *Config {
	cfg := loadGlobalConfig()
	if cfg == nil {
		return &Config{}
	}
	cp := *cfg
	return &cp
}

// Load 加载配置文件
func Load(configPath string) (*Config, error) {
	viper.SetConfigFile(configPath)
	viper.SetConfigType("yaml")

	// 环境变量覆盖:AGENT_MESH_DATABASE_PASSWORD → database.password
	// 便于容器化部署时不把密码写进 config.yaml
	viper.SetEnvPrefix("AGENT_MESH")
	viper.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	viper.AutomaticEnv()

	// 设置默认值
	viper.SetDefault("server.port", 8080)
	viper.SetDefault("server.mode", "debug")
	viper.SetDefault("database.host", "localhost")
	viper.SetDefault("database.port", 3306)
	viper.SetDefault("database.charset", "utf8mb4")
	viper.SetDefault("redis.host", "localhost")
	viper.SetDefault("redis.port", 6379)
	viper.SetDefault("jwt.expire_hours", 24)
	viper.SetDefault("rate_limit.default_qps", 100)
	viper.SetDefault("rate_limit.enabled", true)
	viper.SetDefault("async_mq.type", "memory")
	viper.SetDefault("async_mq.topic", "agent-gateway.async-task")
	viper.SetDefault("async_mq.group_id", "agent-gateway-reliable-worker")
	viper.SetDefault("async_mq.queue_buffer", 4096)
	viper.SetDefault("log.level", "info")
	viper.SetDefault("log.format", "json")
	viper.SetDefault("telemetry.service_name", "agent-gateway")
	viper.SetDefault("telemetry.sample_rate", 0.1)
	viper.SetDefault("nacos.agent_registry_service", "agent-registry")

	if err := viper.ReadInConfig(); err != nil {
		return nil, fmt.Errorf("read config file failed: %w", err)
	}

	var cfg Config
	if err := viper.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("unmarshal config failed: %w", err)
	}

	storeGlobalConfig(&cfg)
	return &cfg, nil
}
