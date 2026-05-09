package service

import (
	"context"
	"fmt"
	"hash/fnv"
	"net/http"
	"sync"
	"time"

	"agent-gateway/internal/model"
	"agent-gateway/internal/repo"
	"agent-gateway/pkg/discovery"
	"agent-gateway/pkg/logger"

	"go.uber.org/zap"
)

const (
	proberInterval      = 15 * time.Second
	proberHTTPTimeout   = 3 * time.Second
	proberFailThreshold = 3
	proberHealthPath    = "/health"
)

// AgentProber 通过 HTTP 主动探测 agent /health 端点，替代 agent 主动心跳。
// 多网关实例部署时，按 agentID 一致性 hash 分片，避免重复探测。
type AgentProber struct {
	agentRepo       agentRepoIface
	cache           *AgentCache
	notifier        AgentRegistryNotifier
	instanceWatcher *discovery.InstanceWatcher
	selfIP          string // 本实例 IP，用于分片归属判定

	interval      time.Duration
	failThreshold int
	httpClient    *http.Client

	failCounts sync.Map // map[string]int, agentID → consecutive failures
	stopCh     chan struct{}
	once       sync.Once
}

func NewAgentProber(
	agentRepo agentRepoIface,
	cache *AgentCache,
	notifier AgentRegistryNotifier,
	instanceWatcher *discovery.InstanceWatcher,
	selfIP string,
) *AgentProber {
	return &AgentProber{
		agentRepo:       agentRepo,
		cache:           cache,
		notifier:        notifier,
		instanceWatcher: instanceWatcher,
		selfIP:          selfIP,
		interval:        proberInterval,
		failThreshold:   proberFailThreshold,
		httpClient: &http.Client{
			Timeout: proberHTTPTimeout,
		},
		stopCh: make(chan struct{}),
	}
}

// Start 启动后台探测循环
func (p *AgentProber) Start() {
	p.once.Do(func() {
		go p.loop()
	})
}

// Stop 停止探测循环
func (p *AgentProber) Stop() {
	close(p.stopCh)
}

func (p *AgentProber) loop() {
	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()
	for {
		select {
		case <-p.stopCh:
			return
		case <-ticker.C:
			p.probeAll()
		}
	}
}

func (p *AgentProber) probeAll() {
	ctx, cancel := context.WithTimeout(context.Background(), p.interval)
	defer cancel()

	// 从 DB 列出所有需要探测的 agent（Active + Inactive，后者用于恢复检测）
	agents, _, err := p.agentRepo.List(ctx, repo.AgentFilter{})
	if err != nil {
		logger.Warn("agent prober: list agents failed", zap.Error(err))
		return
	}

	for _, a := range agents {
		if a.Status == model.AgentStatusDraining {
			continue
		}
		if !p.shouldProbe(a.AgentID) {
			continue
		}
		go p.probeOne(ctx, a)
	}
}

func (p *AgentProber) probeOne(ctx context.Context, agent *model.Agent) {
	url := agent.URL + proberHealthPath

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		p.onFailure(ctx, agent, fmt.Sprintf("build request: %v", err))
		return
	}

	resp, err := p.httpClient.Do(req)
	if err != nil {
		p.onFailure(ctx, agent, fmt.Sprintf("http error: %v", err))
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		p.onFailure(ctx, agent, fmt.Sprintf("status %d", resp.StatusCode))
		return
	}

	p.onSuccess(ctx, agent)
}

func (p *AgentProber) onFailure(ctx context.Context, agent *model.Agent, reason string) {
	newCount := p.incrementFailure(agent.AgentID)
	if newCount < p.failThreshold {
		return
	}
	// 已经 Inactive，不重复处理
	if agent.Status != model.AgentStatusActive {
		return
	}

	if err := p.agentRepo.UpdateStatus(ctx, agent.AgentID, model.AgentStatusInactive); err != nil {
		logger.Error("agent prober: update status failed",
			zap.String("agent_id", agent.AgentID), zap.Error(err))
		return
	}
	p.cache.Delete(agent.AgentID)
	if p.notifier != nil {
		if err := p.notifier.DeregisterAgent(ctx, agent.AgentID, agent.URL); err != nil {
			logger.Warn("agent prober: notify deregister failed",
				zap.String("agent_id", agent.AgentID), zap.Error(err))
		}
	}
	logger.Warn("agent prober: agent inactive (health probe failed)",
		zap.String("agent_id", agent.AgentID),
		zap.String("owner", agent.OwnerAppID),
		zap.String("reason", reason),
		zap.Int("consecutive_failures", newCount),
	)
}

func (p *AgentProber) onSuccess(ctx context.Context, agent *model.Agent) {
	p.failCounts.Delete(agent.AgentID)

	// Active 状态无需处理
	if agent.Status == model.AgentStatusActive {
		return
	}

	// 从 Inactive 恢复为 Active
	if err := p.agentRepo.UpdateStatus(ctx, agent.AgentID, model.AgentStatusActive); err != nil {
		logger.Error("agent prober: recover status failed",
			zap.String("agent_id", agent.AgentID), zap.Error(err))
		return
	}
	updated, err := p.agentRepo.GetByAgentID(ctx, agent.AgentID)
	if err != nil {
		logger.Error("agent prober: reload agent failed",
			zap.String("agent_id", agent.AgentID), zap.Error(err))
		return
	}
	p.cache.Set(updated)
	if p.notifier != nil {
		if err := p.notifier.RegisterAgent(ctx, updated); err != nil {
			logger.Warn("agent prober: notify register failed",
				zap.String("agent_id", agent.AgentID), zap.Error(err))
		}
	}
	logger.Info("agent prober: agent recovered via health probe",
		zap.String("agent_id", agent.AgentID),
		zap.String("owner", agent.OwnerAppID),
	)
}

func (p *AgentProber) incrementFailure(agentID string) int {
	for {
		actual, loaded := p.failCounts.LoadOrStore(agentID, 1)
		if !loaded {
			return 1
		}
		cur := actual.(int)
		next := cur + 1
		if p.failCounts.CompareAndSwap(agentID, cur, next) {
			return next
		}
	}
}

// shouldProbe 判断本实例是否应该探测该 agent（一致性 hash 分片）
// 策略：hash(agentID) % count == hash(selfIP) % count
// InstanceWatcher 不可用或只有 1 个实例时，本实例负责全部。
func (p *AgentProber) shouldProbe(agentID string) bool {
	if p.instanceWatcher == nil || p.selfIP == "" {
		return true
	}
	count := p.instanceWatcher.Count()
	if count <= 1 {
		return true
	}
	return hashMod(agentID, count) == hashMod(p.selfIP, count)
}

func hashMod(s string, mod int) int {
	h := fnv.New32a()
	_, _ = h.Write([]byte(s))
	return int(h.Sum32()) % mod
}
