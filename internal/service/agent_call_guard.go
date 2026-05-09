package service

import (
	"encoding/json"
	"sync"
	"time"

	"agent-gateway/config"
	"agent-gateway/pkg/circuitbreaker"
)

// AgentCallGuard centralizes per-agent call governance shared by sync and async paths.
type AgentCallGuard struct {
	breakerFactory circuitbreaker.BreakerFactory

	mu       sync.RWMutex
	breakers map[string]circuitbreaker.Breaker
}

func NewAgentCallGuard(breakerFactory circuitbreaker.BreakerFactory) *AgentCallGuard {
	return &AgentCallGuard{
		breakerFactory: breakerFactory,
		breakers:       make(map[string]circuitbreaker.Breaker),
	}
}

func (g *AgentCallGuard) Execute(agentID string, fn func() (json.RawMessage, error)) (json.RawMessage, error) {
	breaker := g.getOrCreateBreaker(agentID)
	result, err := breaker.Execute(func() (interface{}, error) {
		return fn()
	})
	if err != nil {
		return nil, err
	}
	return result.(json.RawMessage), nil
}

// Reset clears cached breakers so future calls rebuild them with the latest config.
func (g *AgentCallGuard) Reset() {
	g.mu.Lock()
	g.breakers = make(map[string]circuitbreaker.Breaker)
	g.mu.Unlock()
}

func (g *AgentCallGuard) getOrCreateBreaker(agentID string) circuitbreaker.Breaker {
	g.mu.RLock()
	b, ok := g.breakers[agentID]
	g.mu.RUnlock()
	if ok {
		return b
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	if b, ok = g.breakers[agentID]; ok {
		return b
	}

	cfg := circuitbreaker.DefaultConfig()
	if cbCfg := config.GetCircuitBreakerConfig(); cbCfg != nil {
		if cbCfg.ErrorRateThreshold > 0 {
			cfg.ErrorRateThreshold = cbCfg.ErrorRateThreshold
		}
		if cbCfg.MinRequests > 0 {
			cfg.MinRequests = cbCfg.MinRequests
		}
		if cbCfg.MaxRequests > 0 {
			cfg.MaxRequests = cbCfg.MaxRequests
		}
		if cbCfg.RecoveryIntervalS > 0 {
			cfg.Timeout = time.Duration(cbCfg.RecoveryIntervalS) * time.Second
		}
	}
	cfg.Name = "agent:" + agentID
	b = g.breakerFactory.Create(cfg)
	g.breakers[agentID] = b
	return b
}
