package pool

import (
	"sync"
)

// InvokeRequestPool 请求对象池
// 复用 invokeRequest 对象，减少 GC 压力
type InvokeRequestPool struct {
	pool sync.Pool
}

// NewInvokeRequestPool 创建请求对象池
func NewInvokeRequestPool() *InvokeRequestPool {
	return &InvokeRequestPool{
		pool: sync.Pool{
			New: func() interface{} {
				return make(map[string]interface{}, 16)
			},
		},
	}
}

// Get 获取对象
func (p *InvokeRequestPool) Get() map[string]interface{} {
	return p.pool.Get().(map[string]interface{})
}

// Put 归还对象
func (p *InvokeRequestPool) Put(m map[string]interface{}) {
	// 清空 map 内容
	for k := range m {
		delete(m, k)
	}
	p.pool.Put(m)
}

// ResponsePool 响应对象池
type ResponsePool struct {
	pool sync.Pool
}

// NewResponsePool 创建响应对象池
func NewResponsePool() *ResponsePool {
	return &ResponsePool{
		pool: sync.Pool{
			New: func() interface{} {
				return make(map[string]interface{}, 8)
			},
		},
	}
}

// Get 获取对象
func (p *ResponsePool) Get() map[string]interface{} {
	return p.pool.Get().(map[string]interface{})
}

// Put 归还对象
func (p *ResponsePool) Put(m map[string]interface{}) {
	for k := range m {
		delete(m, k)
	}
	p.pool.Put(m)
}
