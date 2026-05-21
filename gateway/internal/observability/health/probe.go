// Package health 暴露 liveness / readiness / startup 三种探针。
//
// 三种探针语义（符合 K8s 约定）：
//
//   - Liveness (/healthz)：进程是否存活、能否处理 HTTP。任何失败都意味着
//     "重启 Pod"，所以不得依赖外部系统。
//   - Readiness (/readyz)：Pod 是否应该接流量。依赖 DB/Redis 是否可用，
//     优雅关闭期间会被翻成 false。
//   - Startup (/startupz)：Pod 是否完成了一次性的启动工作。K8s 用它延后
//     liveness 探测时机，给慢启动留余地。
//
// 检查器通过 AddReadinessCheck 注册，每次 /readyz 请求都会带短超时地跑
// 所有检查器，保持它们廉价。
package health

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// Checker 表示一个 readiness 依赖检查。
type Checker func(ctx context.Context) error

// Probe 聚合探针状态，可并发使用。
type Probe struct {
	started  atomic.Bool
	draining atomic.Bool

	mu       sync.RWMutex
	checkers map[string]Checker
	timeout  time.Duration
}

// New 返回一个 readiness 检查超时默认 2s 的 Probe。
func New() *Probe {
	return &Probe{
		checkers: make(map[string]Checker),
		timeout:  2 * time.Second,
	}
}

// MarkStarted 翻转启动闸门。一次性的启动工作（加载缓存、跑 migration 等）
// 完成后调一次。
func (p *Probe) MarkStarted() { p.started.Store(true) }

// BeginDraining 把 readiness 翻成 false，让 K8s 从 Service 的 endpoints
// 摘掉本 Pod。SIGTERM 收到后、关 listener 前调用。
func (p *Probe) BeginDraining() { p.draining.Store(true) }

// IsDraining 报告当前探针是否处于 draining 状态。
func (p *Probe) IsDraining() bool { return p.draining.Load() }

// AddReadinessCheck 注册一个命名的 readiness 依赖检查。
// name 会出现在 /readyz 的 JSON 响应里，方便排查。
func (p *Probe) AddReadinessCheck(name string, c Checker) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.checkers[name] = c
}

// LivenessHandler 只要进程活着就返回 200。
func (p *Probe) LivenessHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "alive"})
	}
}

// StartupHandler 在 MarkStarted 被调用后返回 200，之前返回 503。
func (p *Probe) StartupHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if p.started.Load() {
			writeJSON(w, http.StatusOK, map[string]string{"status": "started"})
			return
		}
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "starting"})
	}
}

// ReadinessHandler 跑所有注册过的检查器。任一失败或处于 draining 都返 503，
// JSON body 里给出每个检查器的状态。
func (p *Probe) ReadinessHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if p.draining.Load() {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{
				"status": "draining",
			})
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), p.timeout)
		defer cancel()

		p.mu.RLock()
		snapshot := make(map[string]Checker, len(p.checkers))
		for k, v := range p.checkers {
			snapshot[k] = v
		}
		p.mu.RUnlock()

		results := make(map[string]string, len(snapshot))
		allOK := true
		for name, checker := range snapshot {
			if err := checker(ctx); err != nil {
				results[name] = err.Error()
				allOK = false
			} else {
				results[name] = "ok"
			}
		}

		status := http.StatusOK
		body := map[string]any{
			"status": "ready",
			"checks": results,
		}
		if !allOK {
			status = http.StatusServiceUnavailable
			body["status"] = "unready"
		}
		writeJSON(w, status, body)
	}
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
