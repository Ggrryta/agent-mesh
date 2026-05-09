package concurrency

import (
	"context"
)

// NoopController 空实现（禁用并发控制）
type NoopController struct{}

// NewNoopController 创建空实现
func NewNoopController() *NoopController {
	return &NoopController{}
}

// Acquire 不做任何限制，直接通过
func (c *NoopController) Acquire(ctx context.Context, key string) (func(), error) {
	return func() {}, nil
}

// Close 关闭
func (c *NoopController) Close() error {
	return nil
}

// State 返回状态
func (c *NoopController) State() State {
	return State{
		Mode: "disabled",
	}
}
