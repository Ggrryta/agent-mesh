package friendship

import (
	"sync"
	"time"
)

const defaultCacheTTL = 30 * time.Second

// Cache 是好友关系的本地缓存。key 为排序后的 "agentA|agentB"，value 为查询结果 + 过期时间。
// 通过 Redis Pub/Sub 接收失效通知，TTL 兜底防通知丢失。
type Cache struct {
	mu      sync.RWMutex
	entries map[string]*cacheEntry
	ttl     time.Duration
	stop    chan struct{}
}

type cacheEntry struct {
	allowed   bool
	expiresAt time.Time
}

func NewCache(ttl time.Duration) *Cache {
	if ttl <= 0 {
		ttl = defaultCacheTTL
	}
	c := &Cache{
		entries: make(map[string]*cacheEntry),
		ttl:     ttl,
		stop:    make(chan struct{}),
	}
	go c.cleanupLoop()
	return c
}

// Get 查询缓存。返回 (allowed, hit)。
func (c *Cache) Get(key string) (bool, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	e, ok := c.entries[key]
	if !ok {
		return false, false
	}
	if time.Now().After(e.expiresAt) {
		return false, false
	}
	return e.allowed, true
}

// Set 写入缓存。
func (c *Cache) Set(key string, allowed bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[key] = &cacheEntry{
		allowed:   allowed,
		expiresAt: time.Now().Add(c.ttl),
	}
}

// Invalidate 清除指定 key 的缓存条目。
func (c *Cache) Invalidate(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.entries, key)
}

// Stop 停止后台清理。
func (c *Cache) Stop() {
	close(c.stop)
}

func (c *Cache) cleanupLoop() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			c.evictExpired()
		case <-c.stop:
			return
		}
	}
}

func (c *Cache) evictExpired() {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	for k, e := range c.entries {
		if now.After(e.expiresAt) {
			delete(c.entries, k)
		}
	}
}

// PairKey 生成缓存 key：排序后拼接，保证 (a,b) 和 (b,a) 命中同一条目。
func PairKey(a, b string) string {
	if a > b {
		a, b = b, a
	}
	return a + "|" + b
}
