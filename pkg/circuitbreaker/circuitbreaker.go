// Package circuitbreaker 提供可插拔的熔断器组件
// 支持本地熔断（gobreaker）和分布式熔断（Redis）
package circuitbreaker

import (
	"time"

	"github.com/redis/go-redis/v9"
)

// ========== 错误定义 ==========

var (
	// ErrOpenState 熔断器打开（正在熔断）
	ErrOpenState = Error("circuit breaker is open")
	// ErrTooManyRequests 半开状态下请求过多
	ErrTooManyRequests = Error("too many requests in half-open state")
)

// Error 熔断器错误类型
type Error string

func (e Error) Error() string { return string(e) }

// ========== 状态 ==========

// State 熔断器状态
type State int

const (
	StateClosed   State = iota // 正常
	StateOpen                   // 熔断
	StateHalfOpen               // 半开
)

func (s State) String() string {
	switch s {
	case StateClosed:
		return "closed"
	case StateOpen:
		return "open"
	case StateHalfOpen:
		return "half-open"
	default:
		return "unknown"
	}
}

// ========== 配置 ==========

// Backend 熔断器后端类型
type Backend string

const (
	// BackendLocal 本地熔断（单实例，进程内存）
	BackendLocal Backend = "local"
	// BackendRedis 分布式熔断（多实例共享，Redis 存储）
	BackendRedis Backend = "redis"
)

// Config 熔断器配置
type Config struct {
	// Enabled 是否启用
	Enabled bool

	// Backend 后端类型：local / redis（默认 local）
	Backend Backend

	// Name 熔断器名称（用于监控和日志）
	Name string

	// ErrorRateThreshold 触发熔断的错误率（0.5 = 50%）
	ErrorRateThreshold float64

	// MinRequests 触发熔断所需的最小请求数
	MinRequests int

	// Interval 统计窗口
	Interval time.Duration

	// Timeout 熔断后多久尝试恢复（半开）
	Timeout time.Duration

	// MaxRequests 半开状态下允许通过的最大请求数
	MaxRequests int
}

// DefaultConfig 返回默认配置
func DefaultConfig() Config {
	return Config{
		Enabled:            true,
		Backend:            BackendRedis,
		ErrorRateThreshold: 0.5,
		MinRequests:        10,
		Interval:           60 * time.Second,
		Timeout:            30 * time.Second,
		MaxRequests:        5,
	}
}

// ========== 接口定义 ==========

// Breaker 熔断器接口
type Breaker interface {
	// Execute 通过熔断器执行函数
	// 熔断打开时返回 ErrOpenState
	Execute(fn func() (interface{}, error)) (interface{}, error)

	// State 返回当前状态
	State() State

	// Name 返回名称
	Name() string
}

// BreakerFactory 熔断器工厂（用于按 Skill 创建）
type BreakerFactory interface {
	// Create 根据配置创建熔断器
	Create(cfg Config) Breaker
}

// ========== 工厂函数 ==========

// NewBreaker 根据配置创建熔断器
// rdb 仅在 Backend=redis 时使用，传 nil 则自动降级为本地模式
func NewBreaker(cfg Config, rdb *redis.Client) Breaker {
	if !cfg.Enabled {
		return NewNoopBreaker(cfg.Name)
	}
	if cfg.Backend == BackendRedis && rdb != nil {
		return NewRedisBreaker(rdb, cfg)
	}
	return NewGoBreaker(cfg)
}

// NewBreakerFactory 创建熔断器工厂
func NewBreakerFactory(rdb *redis.Client) BreakerFactory {
	return &defaultFactory{rdb: rdb}
}

type defaultFactory struct {
	rdb *redis.Client
}

func (f *defaultFactory) Create(cfg Config) Breaker {
	return NewBreaker(cfg, f.rdb)
}
