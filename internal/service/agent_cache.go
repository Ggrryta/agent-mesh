package service

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"agent-gateway/internal/model"
	"agent-gateway/internal/repo"
	"agent-gateway/pkg/logger"

	"go.uber.org/zap"
)

const (
	agentCacheRefreshInterval = 10 * time.Minute
)

// AgentCache Agent 元数据内存缓存，路由热路径使用
type AgentCache struct {
	agentRepo agentRepoIface
	notifier  AgentRegistryNotifier

	mu    sync.Mutex
	value atomic.Value // stores map[string]*model.Agent

	done chan struct{}
}

func NewAgentCache(agentRepo *repo.AgentRepo) *AgentCache {
	c := &AgentCache{
		agentRepo: agentRepo,
		done:      make(chan struct{}),
	}
	c.value.Store(make(map[string]*model.Agent))
	return c
}

// Start 启动缓存（加载全量 + 订阅 Nacos + 定时刷新兜底）
func (c *AgentCache) Start(ctx context.Context) error {
	if err := c.loadAll(ctx); err != nil {
		return err
	}
	if c.notifier != nil {
		if err := c.notifier.Subscribe(c.onNacosChange); err != nil {
			logger.Warn("agent cache: nacos subscribe failed, relying on periodic refresh", zap.Error(err))
		}
		c.registerAllToNacos(ctx)
	}
	go c.periodicRefresh(ctx)
	logger.Info("agent cache started", zap.Int("loaded", len(c.snapshot())))
	return nil
}

// SetNotifier 设置跨实例通知器（在 Start 之前调用）
func (c *AgentCache) SetNotifier(n AgentRegistryNotifier) {
	c.notifier = n
}

// Stop 停止后台刷新
func (c *AgentCache) Stop() {
	close(c.done)
}

// Get 获取 Active Agent（路由专用，不查库）
func (c *AgentCache) Get(agentID string) (*model.Agent, bool) {
	a, ok := c.snapshot()[agentID]
	if !ok || a.Status != model.AgentStatusActive {
		return nil, false
	}
	return a, true
}

// Set 写入或更新缓存（注册/心跳后调用）
func (c *AgentCache) Set(a *model.Agent) {
	c.mu.Lock()
	old := c.snapshot()
	next := make(map[string]*model.Agent, len(old)+1)
	for k, v := range old {
		next[k] = v
	}
	next[a.AgentID] = a
	c.value.Store(next)
	c.mu.Unlock()
}

// ListActive 返回所有 Active Agent 的快照（MCP tools/list 专用）
func (c *AgentCache) ListActive() []*model.Agent {
	snapshot := c.snapshot()
	out := make([]*model.Agent, 0, len(snapshot))
	for _, a := range snapshot {
		if a.Status == model.AgentStatusActive {
			out = append(out, a)
		}
	}
	return out
}

// Delete 从缓存中移除（注销时调用）
func (c *AgentCache) Delete(agentID string) {
	c.mu.Lock()
	old := c.snapshot()
	if _, ok := old[agentID]; !ok {
		c.mu.Unlock()
		return
	}
	next := make(map[string]*model.Agent, len(old)-1)
	for k, v := range old {
		if k != agentID {
			next[k] = v
		}
	}
	c.value.Store(next)
	c.mu.Unlock()
}

func (c *AgentCache) loadAll(ctx context.Context) error {
	agents, _, err := c.agentRepo.List(ctx, repo.AgentFilter{})
	if err != nil {
		return err
	}
	next := make(map[string]*model.Agent, len(agents))
	for _, a := range agents {
		next[a.AgentID] = a
	}
	c.mu.Lock()
	c.value.Store(next)
	c.mu.Unlock()
	return nil
}

func (c *AgentCache) snapshot() map[string]*model.Agent {
	if v := c.value.Load(); v != nil {
		return v.(map[string]*model.Agent)
	}
	return nil
}

func (c *AgentCache) periodicRefresh(ctx context.Context) {
	ticker := time.NewTicker(agentCacheRefreshInterval)
	defer ticker.Stop()
	for {
		select {
		case <-c.done:
			return
		case <-ticker.C:
			if err := c.loadAll(ctx); err != nil {
				logger.Error("agent cache refresh failed", zap.Error(err))
			}
		}
	}
}

// onNacosChange reconciles local cache against the Nacos instance list.
func (c *AgentCache) onNacosChange(agentIDs []string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	nacosSet := make(map[string]struct{}, len(agentIDs))
	for _, id := range agentIDs {
		nacosSet[id] = struct{}{}
	}

	current := c.snapshot()

	for id := range nacosSet {
		if _, exists := current[id]; !exists {
			agent, err := c.agentRepo.GetByAgentID(ctx, id)
			if err != nil {
				logger.Warn("nacos reconcile: fetch agent failed", zap.String("agent_id", id), zap.Error(err))
				continue
			}
			c.Set(agent)
		}
	}

	for id := range current {
		if _, exists := nacosSet[id]; !exists {
			c.Delete(id)
		}
	}

	logger.Info("agent cache reconciled via nacos",
		zap.Int("nacos_count", len(agentIDs)),
		zap.Int("cache_count", len(c.snapshot())))
}

// registerAllToNacos ensures all active agents are present in Nacos (handles Nacos restart).
func (c *AgentCache) registerAllToNacos(ctx context.Context) {
	snapshot := c.snapshot()
	for _, agent := range snapshot {
		if agent.Status == model.AgentStatusActive {
			if err := c.notifier.RegisterAgent(ctx, agent); err != nil {
				logger.Warn("agent cache: register to nacos failed",
					zap.String("agent_id", agent.AgentID), zap.Error(err))
			}
		}
	}
}
