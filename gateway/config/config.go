// Package config 从环境变量（12-factor）加载 Agent-Mesh Gateway 配置，
// 本地开发可用 YAML 作为默认值。
//
// 所有配置仅在启动时读一次，通过 Get() 暴露。部分字段的热更新由 config
// watcher 另行处理。
package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

// Config 是顶层配置结构体。这里只用基础类型以保持 env 绑定简单；
// 随着项目成长会加入结构化的子配置。
type Config struct {
	// Server
	BusinessAddr string // :8080 — 面向外部用户的业务 API
	AdminAddr    string // :9090 — 管理端口（metrics / pprof / admin）

	// Logging
	LogLevel  string // debug / info / warn / error
	LogFormat string // json / console

	// MySQL
	MySQLDSN             string
	MySQLMaxOpenConns    int
	MySQLMaxIdleConns    int
	MySQLConnMaxLifetime time.Duration

	// Redis
	RedisAddr     string
	RedisPassword string
	RedisDB       int

	// Misc
	ShutdownTimeout   time.Duration
	StartupReadyDelay time.Duration
	PodName           string

	// Auth
	JWTSecret   string        // HS256 签名密钥（所有用户 / agent 认证必需）
	JWTTTL      time.Duration // user JWT 有效期，默认 24h
	JWTAgentTTL time.Duration // agent JWT 有效期，默认 1h（用 API Key 换得）

	// Kafka
	KafkaBrokers string // 逗号分隔的 broker 地址，空则不启用 Kafka
}

// Load 从环境变量读取配置。缺关键值时直接失败退出（fail-fast）。
// 默认值按本地 k3d 开发场景调过。
func Load() (*Config, error) {
	cfg := &Config{
		BusinessAddr:  getEnv("GATEWAY_BUSINESS_ADDR", ":8080"),
		AdminAddr:     getEnv("GATEWAY_ADMIN_ADDR", ":9090"),
		LogLevel:      getEnv("LOG_LEVEL", "info"),
		LogFormat:     getEnv("LOG_FORMAT", "json"),
		MySQLDSN:      getEnv("MYSQL_DSN", ""),
		RedisAddr:     getEnv("REDIS_ADDR", ""),
		RedisPassword: getEnv("REDIS_PASSWORD", ""),
		PodName:       getEnv("POD_NAME", "dev-local"),
		JWTSecret:     getEnv("JWT_SECRET", ""),
		KafkaBrokers:  getEnv("KAFKA_BROKERS", ""),
	}

	var err error
	if cfg.MySQLMaxOpenConns, err = getEnvInt("MYSQL_MAX_OPEN_CONNS", 50); err != nil {
		return nil, err
	}
	if cfg.MySQLMaxIdleConns, err = getEnvInt("MYSQL_MAX_IDLE_CONNS", 10); err != nil {
		return nil, err
	}
	if cfg.MySQLConnMaxLifetime, err = getEnvDuration("MYSQL_CONN_MAX_LIFETIME", 30*time.Minute); err != nil {
		return nil, err
	}
	if cfg.RedisDB, err = getEnvInt("REDIS_DB", 0); err != nil {
		return nil, err
	}
	if cfg.ShutdownTimeout, err = getEnvDuration("SHUTDOWN_TIMEOUT", 60*time.Second); err != nil {
		return nil, err
	}
	if cfg.StartupReadyDelay, err = getEnvDuration("STARTUP_READY_DELAY", 0); err != nil {
		return nil, err
	}
	if cfg.JWTTTL, err = getEnvDuration("JWT_TTL", 24*time.Hour); err != nil {
		return nil, err
	}
	if cfg.JWTAgentTTL, err = getEnvDuration("JWT_AGENT_TTL", 1*time.Hour); err != nil {
		return nil, err
	}

	return cfg, nil
}

func getEnv(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

func getEnvInt(key string, def int) (int, error) {
	s, ok := os.LookupEnv(key)
	if !ok || s == "" {
		return def, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("config: %s must be int, got %q: %w", key, s, err)
	}
	return n, nil
}

func getEnvDuration(key string, def time.Duration) (time.Duration, error) {
	s, ok := os.LookupEnv(key)
	if !ok || s == "" {
		return def, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("config: %s must be duration, got %q: %w", key, s, err)
	}
	return d, nil
}
