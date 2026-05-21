package circuitbreaker

import (
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/sony/gobreaker"
)

var breakerStateGauge = promauto.NewGaugeVec(prometheus.GaugeOpts{
	Namespace: "mesh",
	Subsystem: "breaker",
	Name:      "state",
	Help:      "Circuit breaker state per agent (0=closed, 1=half-open, 2=open).",
}, []string{"agent_id"})

// Config 熔断器配置。
type Config struct {
	MaxRequests   uint32
	Interval      time.Duration
	Timeout       time.Duration
	FailThreshold uint32
}

// DefaultConfig 默认：5 次失败打开，30s 后半开，半开时允许 1 个请求探测。
func DefaultConfig() Config {
	return Config{
		MaxRequests:   1,
		Interval:      60 * time.Second,
		Timeout:       30 * time.Second,
		FailThreshold: 5,
	}
}

// Guard 管理 per-agent 熔断器。用于 push delivery 保护下游。
type Guard struct {
	cfg      Config
	mu       sync.Mutex
	breakers map[string]*gobreaker.CircuitBreaker
}

func NewGuard(cfg Config) *Guard {
	return &Guard{
		cfg:      cfg,
		breakers: make(map[string]*gobreaker.CircuitBreaker),
	}
}

// Execute 在 agentID 的熔断器保护下执行 fn。
// 熔断器打开时直接返回 gobreaker.ErrOpenState。
func (g *Guard) Execute(agentID string, fn func() error) error {
	cb := g.getOrCreate(agentID)
	_, err := cb.Execute(func() (any, error) {
		return nil, fn()
	})
	return err
}

// State 返回 agentID 的熔断器状态。
func (g *Guard) State(agentID string) gobreaker.State {
	cb := g.getOrCreate(agentID)
	return cb.State()
}

func (g *Guard) getOrCreate(agentID string) *gobreaker.CircuitBreaker {
	g.mu.Lock()
	defer g.mu.Unlock()

	if cb, ok := g.breakers[agentID]; ok {
		return cb
	}

	cb := gobreaker.NewCircuitBreaker(gobreaker.Settings{
		Name:        agentID,
		MaxRequests: g.cfg.MaxRequests,
		Interval:    g.cfg.Interval,
		Timeout:     g.cfg.Timeout,
		ReadyToTrip: func(counts gobreaker.Counts) bool {
			return counts.ConsecutiveFailures >= g.cfg.FailThreshold
		},
		OnStateChange: func(name string, from, to gobreaker.State) {
			breakerStateGauge.WithLabelValues(name).Set(float64(to))
		},
	})
	g.breakers[agentID] = cb
	return cb
}
