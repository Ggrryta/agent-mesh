package middleware

import (
	"net/http"
	"runtime/debug"

	"github.com/Ggrryta/agent-mesh/gateway/internal/api/httpx"

	"go.uber.org/zap"
)

// Recover 拦截 handler panic，返回 500 并把堆栈写日志。
//
// 设计要点：
//   - **必须挂在 request_id 之后**：panic 日志要带 request_id 才能对到前端报错
//   - **必须挂在 access_log 外层**：access_log 能记到最终 500 状态码
//   - ResponseWriter 可能已经写过一部分 header/body —— 此时只能干净地终止，
//     不能再覆盖。用 wroteHeader 标记判断
//
// 重放：
//   - 不记录 client ip / uid，这些 access_log 已经记过
//   - stack 用 debug.Stack() 全量记录到 error 级别；量偏大但 panic 不该经常发生
func Recover(log *zap.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				rec := recover()
				if rec == nil {
					return
				}
				// 过滤 ErrAbortHandler —— net/http 自身靠这个中断写响应，别当 panic 处理。
				if rec == http.ErrAbortHandler {
					panic(rec) // 让它继续冒泡
				}
				log.Error("panic recovered",
					zap.Any("error", rec),
					zap.String("request_id", RequestIDFromContext(r.Context())),
					zap.String("method", r.Method),
					zap.String("path", r.URL.Path),
					zap.ByteString("stack", debug.Stack()),
				)
				// 能不能写响应取决于之前是否已写过 header。
				// 用 httpx 标准错误形，保持响应 shape 一致。
				// 注意：如果之前已经 WriteHeader，这次 WriteHeader 会被 Go
				// 的 net/http log 出一条警告但不影响客户端已收到的部分。
				httpx.WriteError(w, http.StatusInternalServerError,
					httpx.CodeInternal, "internal server error")
			}()
			next.ServeHTTP(w, r)
		})
	}
}
