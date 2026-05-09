package service

import (
	"net/http"
	"sync"
	"time"

	"agent-gateway/pkg/logger"

	"go.uber.org/zap"
)

const (
	clientIdleEvictInterval = 5 * time.Minute
	clientIdleEvictTimeout  = 10 * time.Minute
)

// A2AClientPool 按 upstream URL 缓存 http.Client，复用 TCP 连接池
type A2AClientPool struct {
	mu       sync.RWMutex
	clients  map[string]*http.Client
	lastUsed map[string]time.Time
	done     chan struct{}
}

func NewA2AClientPool() *A2AClientPool {
	return &A2AClientPool{
		clients:  make(map[string]*http.Client),
		lastUsed: make(map[string]time.Time),
		done:     make(chan struct{}),
	}
}

// Get 获取或创建指定 upstream URL 的 http.Client
func (p *A2AClientPool) Get(upstreamURL string) *http.Client {
	p.mu.RLock()
	c, ok := p.clients[upstreamURL]
	p.mu.RUnlock()
	if ok {
		p.mu.Lock()
		p.lastUsed[upstreamURL] = time.Now()
		p.mu.Unlock()
		return c
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if c, ok = p.clients[upstreamURL]; ok {
		p.lastUsed[upstreamURL] = time.Now()
		return c
	}
	c = &http.Client{
		Timeout: 60 * time.Second,
		Transport: &http.Transport{
			MaxIdleConnsPerHost: 32,
			IdleConnTimeout:     90 * time.Second,
		},
	}
	p.clients[upstreamURL] = c
	p.lastUsed[upstreamURL] = time.Now()
	return c
}

// StartCleanup 启动定时清理，移除长时间无请求的 Client
func (p *A2AClientPool) StartCleanup() {
	go func() {
		ticker := time.NewTicker(clientIdleEvictInterval)
		defer ticker.Stop()
		for {
			select {
			case <-p.done:
				return
			case <-ticker.C:
				p.evictIdle()
			}
		}
	}()
}

// Stop 停止清理
func (p *A2AClientPool) Stop() {
	close(p.done)
}

func (p *A2AClientPool) evictIdle() {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now()
	for url, last := range p.lastUsed {
		if now.Sub(last) > clientIdleEvictTimeout {
			if c, ok := p.clients[url]; ok {
				c.CloseIdleConnections()
			}
			delete(p.clients, url)
			delete(p.lastUsed, url)
			logger.Info("a2a client pool: evicted idle client", zap.String("url", url))
		}
	}
}
