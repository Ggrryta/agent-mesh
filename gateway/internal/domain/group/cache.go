package group

import (
	"sync"
	"time"
)

const defaultCacheTTL = 30 * time.Second

// Cache 是群组关系的本地缓存。key 为排序后的 "agentA|agentB"，value 为 SameGroup 结果 + 过期时间。
type Cache struct {
	mu      sync.RWMutex
	entries map[string]*cacheEntry
	ttl     time.Duration
	stop    chan struct{}
}

type cacheEntry struct {
	sameGroup bool
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
	return e.sameGroup, true
}

func (c *Cache) Set(key string, sameGroup bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[key] = &cacheEntry{
		sameGroup: sameGroup,
		expiresAt: time.Now().Add(c.ttl),
	}
}

func (c *Cache) Invalidate(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.entries, key)
}

// InvalidateAgent 清除涉及该 agent 的所有缓存条目（加人/踢人时用）。
func (c *Cache) InvalidateAgent(agentID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k := range c.entries {
		if containsAgent(k, agentID) {
			delete(c.entries, k)
		}
	}
}

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

// PairKey 生成缓存 key：排序后拼接。
func PairKey(a, b string) string {
	if a > b {
		a, b = b, a
	}
	return a + "|" + b
}

func containsAgent(key, agentID string) bool {
	// key 格式为 "agentA|agentB"
	return len(key) > len(agentID) &&
		(key[:len(agentID)] == agentID && key[len(agentID)] == '|' ||
			key[len(key)-len(agentID):] == agentID && key[len(key)-len(agentID)-1] == '|')
}
