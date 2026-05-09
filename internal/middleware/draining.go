package middleware

import (
	"context"
	"sync/atomic"

	"agent-gateway/pkg/resp"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
)

// Draining 优雅停止中间件
// 在停止期间主动拒绝新请求，返回 503
type Draining struct {
	isDraining atomic.Bool
}

// NewDraining 创建 Draining 中间件
func NewDraining() *Draining {
	return &Draining{}
}

// Middleware 中间件
func (d *Draining) Middleware() app.HandlerFunc {
	return func(c context.Context, ctx *app.RequestContext) {
		if d.isDraining.Load() {
			ctx.AbortWithStatusJSON(consts.StatusServiceUnavailable, resp.Err(503, "server is draining, please retry later"))
			return
		}
		ctx.Next(c)
	}
}

// Start 开始拒绝新请求
func (d *Draining) Start() {
	d.isDraining.Store(true)
}

// IsDraining 是否正在停止
func (d *Draining) IsDraining() bool {
	return d.isDraining.Load()
}
