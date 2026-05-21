// Package delivery 负责"把 inbox 事件尽力而为地 push 到 agent"。
//
// 定位（详见 ADR 010）：
//   - push 是 **优化** 路径，不是真相之源
//   - 失败不重试 —— 下次 agent 主动拉 inbox 会看到；或下次 task 动作触发时
//     再推一次
//   - push 请求是**异步**发出的：task.Service 调 inbox.Enqueue 得到 event
//     后立刻通过 channel 通知 pushWorker，push 在后台 goroutine 跑，
//     API 请求不阻塞
//
// 部署形态：
//   - 嵌在 gateway 进程内，一个 Pod 一个 pushWorker
//   - 多副本时每个副本都尝试 push —— agent 按事件 id 去重，没关系
//   - 未来量大时可以加"CAS claim event 再 push"，现在不做
package delivery

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/Ggrryta/agent-mesh/gateway/internal/circuitbreaker"
	"github.com/Ggrryta/agent-mesh/gateway/internal/domain/inbox"
	"github.com/Ggrryta/agent-mesh/gateway/internal/observability/metrics"

	"go.uber.org/zap"
)

// AgentURLLookup 让 Pusher 知道 agent 的 URL。
// 由 agent.Service 实现（agents 表里的 url 字段）。
type AgentURLLookup interface {
	// LookupURL 返回 agent 的 URL；agent 没登记 URL 时 ok=false。
	LookupURL(ctx context.Context, agentID string) (url string, ok bool)
}

// Pusher 是一个后台 worker：从 channel 接收完整 event，尝试 POST 到 agent URL。
type Pusher struct {
	inbox    *inbox.Service
	agents   AgentURLLookup
	breaker  *circuitbreaker.Guard
	log      *zap.Logger
	client   *http.Client
	httpPath string        // URL suffix，默认 /a2a/events
	timeout  time.Duration // 单次 push 超时
	events   chan *inbox.Event

	// 测试用：doFunc 非 nil 时替代 client.Do，允许注入行为（例如强制失败）。
	doFunc func(req *http.Request) (*http.Response, error)
}

// Config 是 Pusher 的可调参数。
type Config struct {
	HTTPPath   string        // 默认 /a2a/events
	Timeout    time.Duration // 默认 3s
	QueueDepth int           // 默认 1024
}

func (c *Config) normalize() {
	if c.HTTPPath == "" {
		c.HTTPPath = "/a2a/events"
	}
	if c.Timeout <= 0 {
		c.Timeout = 3 * time.Second
	}
	if c.QueueDepth <= 0 {
		c.QueueDepth = 1024
	}
}

// NewPusher 装配一个 Pusher。
func NewPusher(inboxSvc *inbox.Service, agents AgentURLLookup, cfg Config, log *zap.Logger) *Pusher {
	cfg.normalize()
	if log == nil {
		log = zap.NewNop()
	}
	return &Pusher{
		inbox:    inboxSvc,
		agents:   agents,
		log:      log,
		client:   &http.Client{Timeout: cfg.Timeout},
		httpPath: cfg.HTTPPath,
		timeout:  cfg.Timeout,
		events:   make(chan *inbox.Event, cfg.QueueDepth),
	}
}

// WithBreaker 注入熔断器，保护下游 agent push。
func (p *Pusher) WithBreaker(g *circuitbreaker.Guard) *Pusher {
	p.breaker = g
	return p
}

// NotifyEvent 告诉 pusher "有一个新的 event"。
// 非阻塞 —— 队列满时直接丢弃，打 warn 日志。agent 下次拉 inbox 会自然收到。
func (p *Pusher) NotifyEvent(event *inbox.Event) {
	if event == nil {
		return
	}
	select {
	case p.events <- event:
	default:
		p.log.Warn("push queue full, event will be delivered on next pull",
			zap.Int64("event_id", event.ID),
			zap.String("agent_id", event.AgentID))
	}
}

// Run 阻塞跑 worker loop，直到 ctx.Done。通常由 main.go 起一个 goroutine。
func (p *Pusher) Run(ctx context.Context) {
	p.log.Info("push worker started",
		zap.String("path", p.httpPath),
		zap.Duration("timeout", p.timeout))
	for {
		select {
		case <-ctx.Done():
			p.log.Info("push worker exiting")
			return
		case e := <-p.events:
			p.handleOne(ctx, e)
		}
	}
}

// handleOne 处理一个 event：尝试 push，成功则 MarkDelivered。
// 所有失败都只打日志，不传出 —— push 是 best-effort。
func (p *Pusher) handleOne(ctx context.Context, event *inbox.Event) {
	// 熔断器保护：agent 连续 push 失败时快速跳过，不浪费连接。
	if p.breaker != nil {
		err := p.breaker.Execute(event.AgentID, func() error {
			ok, pushErr := p.push(ctx, event)
			if pushErr != nil {
				return pushErr
			}
			if ok {
				p.markDelivered(event)
			}
			return nil
		})
		if err != nil {
			if !errors.Is(err, errNoURL) {
				metrics.InboxPushFailTotal.Inc()
				p.log.Debug("push breaker", zap.String("agent_id", event.AgentID), zap.Error(err))
			}
		} else {
			metrics.InboxPushSuccessTotal.Inc()
		}
		return
	}

	// 无熔断器时走原有逻辑
	ok, err := p.push(ctx, event)
	if err != nil {
		if errors.Is(err, errNoURL) {
			p.log.Debug("skip push: no URL",
				zap.String("agent_id", event.AgentID),
				zap.Int64("event_id", event.ID))
			return
		}
		metrics.InboxPushFailTotal.Inc()
		p.log.Warn("push failed, will be delivered on next pull",
			zap.String("agent_id", event.AgentID),
			zap.Int64("event_id", event.ID),
			zap.Error(err))
		return
	}
	if ok {
		metrics.InboxPushSuccessTotal.Inc()
		p.markDelivered(event)
	}
}

func (p *Pusher) markDelivered(event *inbox.Event) {
	markCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := p.inbox.MarkDelivered(markCtx, []int64{event.ID}); err != nil {
		p.log.Warn("mark delivered failed",
			zap.Int64("event_id", event.ID), zap.Error(err))
	}
}

// push 实际执行一次 push 尝试。
// 返回 (推送成功?, error)。err=errNoURL 表"跳过"，其它为真失败。
func (p *Pusher) push(ctx context.Context, event *inbox.Event) (bool, error) {
	url, ok := p.agents.LookupURL(ctx, event.AgentID)
	if !ok || url == "" {
		return false, errNoURL
	}
	full := url + p.httpPath

	body, err := json.Marshal(event)
	if err != nil {
		return false, fmt.Errorf("marshal event: %w", err)
	}

	reqCtx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, full, bytes.NewReader(body))
	if err != nil {
		return false, fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	do := p.doFunc
	if do == nil {
		do = p.client.Do
	}
	resp, err := do(req)
	if err != nil {
		return false, fmt.Errorf("http: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return false, fmt.Errorf("status %d", resp.StatusCode)
	}
	return true, nil
}

// errNoURL 表明 agent 没登记 URL，无法 push；不是失败，是"跳过"。
var errNoURL = errors.New("delivery: agent has no URL")
