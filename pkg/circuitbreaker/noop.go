package circuitbreaker

// NoopBreaker 空实现（禁用熔断）
type NoopBreaker struct {
	name string
}

// NewNoopBreaker 创建空实现
func NewNoopBreaker(name string) *NoopBreaker {
	return &NoopBreaker{name: name}
}

// Execute 直接执行函数，不做任何熔断控制
func (b *NoopBreaker) Execute(fn func() (interface{}, error)) (interface{}, error) {
	return fn()
}

// State 返回关闭状态
func (b *NoopBreaker) State() State {
	return StateClosed
}

// Name 返回名称
func (b *NoopBreaker) Name() string {
	return b.name
}
