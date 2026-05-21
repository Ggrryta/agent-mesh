package outbox

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/Ggrryta/agent-mesh/gateway/internal/observability/metrics"

	"go.uber.org/zap"
)

const (
	maxRetries     = 10
	batchSize      = 10 // 每个 worker 每轮每 agent 处理的事件数
	pollInterval   = 5 * time.Second // 兜底轮询（大部分事件已被乐观直发处理）
	baseRetryDelay = 5 * time.Second
	heartbeatInterval = 5 * time.Second
	podExpiry         = 30 * time.Second
	topologyPause     = 2 * time.Second
)

// Handler 处理 outbox 事件的回调。
type Handler func(ctx context.Context, event *Event) error

// DeadLetterNotifier 在事件进入死信时通知发送方。可为 nil。
type DeadLetterNotifier func(ctx context.Context, event *Event, errMsg string)

// DispatcherConfig 配置。
type DispatcherConfig struct {
	Workers int    // Pod 内 worker goroutine 数量，默认 4
	PodID   string // 本 Pod 标识，默认用 hostname
}

// Dispatcher 多实例安全的 outbox 事件分发器。
// 通过 hash(target_agent_id) 分片保证 per-agent 有序。
type Dispatcher struct {
	repo     *SQLRepo
	handler  Handler
	notifier DeadLetterNotifier
	config   DispatcherConfig
	log      *zap.Logger

	mu        sync.RWMutex
	podIndex  int
	totalPods int
}

func NewDispatcher(repo *SQLRepo, handler Handler, log *zap.Logger) *Dispatcher {
	podID, _ := os.Hostname()
	if podID == "" {
		podID = fmt.Sprintf("pod-%d", time.Now().UnixNano()%10000)
	}
	return &Dispatcher{
		repo:      repo,
		handler:   handler,
		config:    DispatcherConfig{Workers: 4, PodID: podID},
		totalPods: 1,
		log:       log,
	}
}

// WithConfig 设置配置。
func (d *Dispatcher) WithConfig(cfg DispatcherConfig) *Dispatcher {
	if cfg.Workers > 0 {
		d.config.Workers = cfg.Workers
	}
	if cfg.PodID != "" {
		d.config.PodID = cfg.PodID
	}
	return d
}

// WithDeadLetterNotifier 设置死信通知回调。
func (d *Dispatcher) WithDeadLetterNotifier(n DeadLetterNotifier) { d.notifier = n }

// Run 启动 dispatcher：心跳 + N 个 worker goroutine。
func (d *Dispatcher) Run(ctx context.Context) {
	d.log.Info("outbox dispatcher started",
		zap.String("pod_id", d.config.PodID),
		zap.Int("workers", d.config.Workers))

	// 注册 Pod + 启动心跳
	go d.heartbeatLoop(ctx)

	// 等待首次心跳完成（获取拓扑信息）
	time.Sleep(100 * time.Millisecond)

	// 启动 worker goroutines
	var wg sync.WaitGroup
	for i := 0; i < d.config.Workers; i++ {
		wg.Add(1)
		go func(workerIndex int) {
			defer wg.Done()
			d.workerLoop(ctx, workerIndex)
		}(i)
	}
	wg.Wait()
}

// heartbeatLoop 定期心跳 + 检测拓扑变化。
func (d *Dispatcher) heartbeatLoop(ctx context.Context) {
	d.refreshTopology(ctx)
	ticker := time.NewTicker(heartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.refreshTopology(ctx)
		}
	}
}

func (d *Dispatcher) refreshTopology(ctx context.Context) {
	// 心跳
	d.repo.PodHeartbeat(ctx, d.config.PodID)

	// 获取活跃 Pod 列表
	pods, err := d.repo.ListActivePods(ctx, podExpiry)
	if err != nil {
		d.log.Warn("dispatcher: list pods failed", zap.Error(err))
		return
	}

	// 计算自己的 index（按 pod_id 排序确定顺序）
	newTotal := len(pods)
	newIndex := 0
	for i, p := range pods {
		if p == d.config.PodID {
			newIndex = i
			break
		}
	}

	d.mu.Lock()
	oldTotal := d.totalPods
	d.totalPods = newTotal
	d.podIndex = newIndex
	d.mu.Unlock()

	if oldTotal != newTotal {
		d.log.Info("dispatcher: topology changed",
			zap.Int("old_pods", oldTotal),
			zap.Int("new_pods", newTotal),
			zap.Int("my_index", newIndex))
		// 暂停让旧拓扑的处理完成
		time.Sleep(topologyPause)
	}
}

// workerLoop 单个 worker 的处理循环。
func (d *Dispatcher) workerLoop(ctx context.Context, workerIndex int) {
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.processMySlot(ctx, workerIndex)
		}
	}
}

// processMySlot 处理属于本 worker 的 hash 槽位的事件。
func (d *Dispatcher) processMySlot(ctx context.Context, workerIndex int) {
	d.mu.RLock()
	totalPods := d.totalPods
	podIndex := d.podIndex
	d.mu.RUnlock()

	workers := d.config.Workers
	events, err := d.repo.ClaimByHashSlot(ctx, totalPods, podIndex, workers, workerIndex, batchSize)
	if err != nil {
		d.log.Warn("outbox: claim by slot", zap.Error(err), zap.Int("worker", workerIndex))
		return
	}
	for _, e := range events {
		if err := d.handler(ctx, e); err != nil {
			d.handleFailure(ctx, e, err)
			break // 保序：失败后不跳过
		}
		if err := d.repo.MarkSent(ctx, e.ID); err != nil {
			d.log.Warn("outbox: mark sent", zap.Int64("id", e.ID), zap.Error(err))
		}
	}
}

func (d *Dispatcher) handleFailure(ctx context.Context, e *Event, err error) {
	d.log.Warn("outbox: handler failed",
		zap.Int64("id", e.ID),
		zap.String("type", e.EventType),
		zap.Int("retries", e.Retries),
		zap.Error(err))

	if e.Retries >= maxRetries {
		errMsg := err.Error()
		if moveErr := d.repo.MoveToDeadLetter(ctx, e, errMsg); moveErr != nil {
			d.log.Error("outbox: move to dead letter failed", zap.Int64("id", e.ID), zap.Error(moveErr))
			d.repo.MarkFailed(ctx, e.ID)
			return
		}
		metrics.OutboxDeadLetterTotal.Inc()
		d.log.Error("outbox: event moved to dead letter",
			zap.Int64("id", e.ID),
			zap.String("type", e.EventType),
			zap.String("error", errMsg))
		if d.notifier != nil {
			d.notifier(ctx, e, errMsg)
		}
		return
	}
	delay := baseRetryDelay * time.Duration(1<<e.Retries)
	nextRetry := time.Now().Add(delay)
	d.repo.IncrRetry(ctx, e.ID, nextRetry)
}

// Publish 写一条事件到 outbox 表。
func Publish(ctx context.Context, repo Repo, eventType string, payload any) (*Event, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return repo.Insert(ctx, eventType, data)
}
