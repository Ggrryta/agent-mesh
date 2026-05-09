package concurrency

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

// LocalController 本地并发控制器
// 使用 channel 实现，适用于单实例场景
type LocalController struct {
	cfg      Config
	sem      chan struct{}
	mu       sync.RWMutex
	acquired int64
}

// NewLocalController 创建本地并发控制器
func NewLocalController(cfg Config) *LocalController {
	cfg.normalize()
	return &LocalController{
		cfg:  cfg,
		sem: make(chan struct{}, cfg.MaxConcurrency),
	}
}

// Acquire 获取并发槽位
func (c *LocalController) Acquire(ctx context.Context, key string) (func(), error) {
	// 不排队：立即获取
	if c.cfg.QueueTimeout <= 0 {
		select {
		case c.sem <- struct{}{}:
			atomic.AddInt64(&c.acquired, 1)
			return func() {
				atomic.AddInt64(&c.acquired, -1)
				<-c.sem
			}, nil
		default:
			return nil, ErrQueueTimeout
		}
	}

	// 排队：等待指定时间
	select {
	case c.sem <- struct{}{}:
		atomic.AddInt64(&c.acquired, 1)
		return func() {
			atomic.AddInt64(&c.acquired, -1)
			<-c.sem
		}, nil
	case <-time.After(c.cfg.QueueTimeout):
		return nil, ErrQueueTimeout
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Close 关闭控制器
func (c *LocalController) Close() error {
	return nil
}

// State 返回当前状态
func (c *LocalController) State() State {
	return State{
		Mode:       "local",
		Total:      c.cfg.MaxConcurrency,
		Available:  c.cfg.MaxConcurrency - int(atomic.LoadInt64(&c.acquired)),
		Acquired:   int(atomic.LoadInt64(&c.acquired)),
	}
}
