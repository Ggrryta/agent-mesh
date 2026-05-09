package circuitbreaker

import (
	"github.com/sony/gobreaker/v2"
)

// GoBreaker 基于 sony/gobreaker 的熔断器实现
type GoBreaker struct {
	cb *gobreaker.CircuitBreaker[interface{}]
}

// NewGoBreaker 创建基于 gobreaker 的熔断器
func NewGoBreaker(cfg Config) *GoBreaker {
	settings := gobreaker.Settings{
		Name:        cfg.Name,
		MaxRequests: uint32(cfg.MaxRequests),
		Interval:    cfg.Interval,
		Timeout:     cfg.Timeout,
		ReadyToTrip: func(counts gobreaker.Counts) bool {
			if counts.Requests < uint32(cfg.MinRequests) {
				return false
			}
			failRate := float64(counts.TotalFailures) / float64(counts.Requests)
			return failRate >= cfg.ErrorRateThreshold
		},
	}

	return &GoBreaker{
		cb: gobreaker.NewCircuitBreaker[interface{}](settings),
	}
}

// Execute 通过熔断器执行函数
func (b *GoBreaker) Execute(fn func() (interface{}, error)) (interface{}, error) {
	return b.cb.Execute(fn)
}

// State 返回当前状态
func (b *GoBreaker) State() State {
	switch b.cb.State() {
	case gobreaker.StateClosed:
		return StateClosed
	case gobreaker.StateOpen:
		return StateOpen
	case gobreaker.StateHalfOpen:
		return StateHalfOpen
	default:
		return StateClosed
	}
}

// Name 返回名称
func (b *GoBreaker) Name() string {
	return b.cb.Name()
}

// Counts 返回计数（用于监控）
func (b *GoBreaker) Counts() gobreaker.Counts {
	return b.cb.Counts()
}

// ========== 状态转换辅助 ==========

// StateFromGoBreaker 将 gobreaker 状态转为 circuitbreaker 状态
func StateFromGoBreaker(s gobreaker.State) State {
	switch s {
	case gobreaker.StateClosed:
		return StateClosed
	case gobreaker.StateOpen:
		return StateOpen
	case gobreaker.StateHalfOpen:
		return StateHalfOpen
	default:
		return StateClosed
	}
}
