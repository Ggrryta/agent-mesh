// Package cache 提供带 TTL 的本地内存缓存，用于减少热路径上的 DB 查询
package cache

import (
	"sync"
	"time"
)

type entry[V any] struct {
	value     V
	expiresAt time.Time
}

// TTLCache 线程安全的本地内存缓存，支持 TTL 自动过期
type TTLCache[K comparable, V any] struct {
	mu      sync.RWMutex
	items   map[K]entry[V]
	ttl     time.Duration
}

// New 创建 TTLCache，ttl 为每条记录的存活时间
func New[K comparable, V any](ttl time.Duration) *TTLCache[K, V] {
	return &TTLCache[K, V]{
		items: make(map[K]entry[V]),
		ttl:   ttl,
	}
}

// Get 返回缓存值，miss 或已过期返回零值和 false
func (c *TTLCache[K, V]) Get(key K) (V, bool) {
	c.mu.RLock()
	e, ok := c.items[key]
	c.mu.RUnlock()
	if !ok || time.Now().After(e.expiresAt) {
		var zero V
		return zero, false
	}
	return e.value, true
}

// Set 写入缓存，使用构造时指定的 TTL
func (c *TTLCache[K, V]) Set(key K, value V) {
	c.mu.Lock()
	c.items[key] = entry[V]{value: value, expiresAt: time.Now().Add(c.ttl)}
	c.mu.Unlock()
}

// Delete 主动删除一条记录（key 变更时使用）
func (c *TTLCache[K, V]) Delete(key K) {
	c.mu.Lock()
	delete(c.items, key)
	c.mu.Unlock()
}
