package agent

import (
	"context"
	"sync"
	"time"
)

// Cache 是 agent 在进程内的只读视图。读路径用 RWMutex 保护，多读者并行；
// 写路径独占锁，O(1) 修改单个 key。
//
// Cache 不是权威数据，MySQL 才是。启动时从 DB 全量拉，写方发现漂移时
// 也会触发重建。
type Cache struct {
	mu      sync.RWMutex
	entries map[string]*Agent
}

// NewCache 返回一个空 cache。对外服务前要先调用 Reload 预热。
func NewCache() *Cache {
	return &Cache{
		entries: make(map[string]*Agent),
	}
}

// Reload 用新列表替换全部数据。启动时和定时任务里调用。
func (c *Cache) Reload(agents []*Agent) {
	m := make(map[string]*Agent, len(agents))
	for _, a := range agents {
		m[a.AgentID] = a
	}
	c.mu.Lock()
	c.entries = m
	c.mu.Unlock()
}

// Get 按 ID 取 agent，第二个返回值指示是否存在。
func (c *Cache) Get(id string) (*Agent, bool) {
	c.mu.RLock()
	a, ok := c.entries[id]
	c.mu.RUnlock()
	return a, ok
}

// GetActive 在 Get 基础上加 Status 过滤，让调用方少一层分支。
// 从 router 视角看，inactive / draining 的 agent 等同于"不存在"。
func (c *Cache) GetActive(id string) (*Agent, bool) {
	a, ok := c.Get(id)
	if !ok || a.Status != StatusActive {
		return nil, false
	}
	return a, true
}

// Set 替换或插入一个 agent。注册路径用它让新 agent 立刻可被路由到。
func (c *Cache) Set(a *Agent) {
	c.mu.Lock()
	c.entries[a.AgentID] = a
	c.mu.Unlock()
}

// Delete 从 cache 中移除一个 agent。
func (c *Cache) Delete(id string) {
	c.mu.Lock()
	delete(c.entries, id)
	c.mu.Unlock()
}

// Len 方便指标和测试用。
func (c *Cache) Len() int {
	c.mu.RLock()
	n := len(c.entries)
	c.mu.RUnlock()
	return n
}

// Each 在一致的 snapshot 上迭代。
// 持有 RLock 期间回调不应做耗时操作。
func (c *Cache) Each(fn func(a *Agent) bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, a := range c.entries {
		if !fn(a) {
			return
		}
	}
}

// Reloader 把周期性全量 reload 装进一个后台 goroutine。
// 防御式实现：某次 List 失败时保留现有数据不动。
type Reloader struct {
	cache    *Cache
	load     func(context.Context) ([]*Agent, error)
	interval time.Duration
	done     chan struct{}
}

// NewReloader 接收一个 load 函数（通常由 Repo.List 支撑）和 interval。
func NewReloader(c *Cache, load func(context.Context) ([]*Agent, error), interval time.Duration) *Reloader {
	if interval <= 0 {
		interval = 10 * time.Minute
	}
	return &Reloader{cache: c, load: load, interval: interval, done: make(chan struct{})}
}

// Run 阻塞运行，直到 ctx 被取消或 Stop 被调用。调用方应放进 goroutine。
func (r *Reloader) Run(ctx context.Context) {
	t := time.NewTicker(r.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-r.done:
			return
		case <-t.C:
			rctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			agents, err := r.load(rctx)
			cancel()
			if err == nil {
				r.cache.Reload(agents)
			}
		}
	}
}

// Stop 通知 Run 退出。
func (r *Reloader) Stop() {
	close(r.done)
}
