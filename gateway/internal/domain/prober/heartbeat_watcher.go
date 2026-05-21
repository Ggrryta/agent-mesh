// Package prober 的 heartbeat_watcher.go：定时扫描心跳超时的 agent，标记 inactive。
//
// 与 Prober 互补：
//   - Prober 主动探测有 URL 的 agent（适用于 Gateway 可达 meshd 的场景）
//   - HeartbeatWatcher 被动检测心跳超时（适用于任何场景，包括 NAT 后面的 meshd）
//
// 设计：
//   - 每 30s 扫描一次 agents 表
//   - 条件：status='active' AND last_heartbeat_at < NOW() - gracePeriod
//   - 命中 → UPDATE status='inactive'
//   - 多副本安全：用 CAS（WHERE status='active'）防止重复翻转，多副本同时跑不冲突
package prober

import (
	"context"
	"database/sql"
	"time"

	"go.uber.org/zap"
)

// HeartbeatWatcherConfig 心跳超时检测配置。
type HeartbeatWatcherConfig struct {
	// ScanInterval 扫描间隔。默认 30s。
	ScanInterval time.Duration
	// GracePeriod 心跳超时宽限期。默认 90s（心跳间隔 30s × 3 次机会）。
	GracePeriod time.Duration
}

func (c *HeartbeatWatcherConfig) normalize() {
	if c.ScanInterval <= 0 {
		c.ScanInterval = 30 * time.Second
	}
	if c.GracePeriod <= 0 {
		c.GracePeriod = 90 * time.Second
	}
}

// HeartbeatWatcher 定时扫描心跳超时的 agent 并标记 inactive。
type HeartbeatWatcher struct {
	db  *sql.DB
	cfg HeartbeatWatcherConfig
	log *zap.Logger
}

func NewHeartbeatWatcher(db *sql.DB, cfg HeartbeatWatcherConfig, log *zap.Logger) *HeartbeatWatcher {
	cfg.normalize()
	return &HeartbeatWatcher{db: db, cfg: cfg, log: log}
}

// Run 阻塞运行直到 ctx 取消。
func (w *HeartbeatWatcher) Run(ctx context.Context) {
	t := time.NewTicker(w.cfg.ScanInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			w.scan(ctx)
		}
	}
}

func (w *HeartbeatWatcher) scan(ctx context.Context) {
	res, err := w.db.ExecContext(ctx, `
		UPDATE agents
		SET status = 'inactive'
		WHERE status = 'active'
		  AND kind = 'normal'
		  AND last_heartbeat_at IS NOT NULL
		  AND last_heartbeat_at < NOW() - INTERVAL ? SECOND`,
		int(w.cfg.GracePeriod.Seconds()),
	)
	if err != nil {
		w.log.Warn("heartbeat watcher: scan failed", zap.Error(err))
		return
	}
	n, _ := res.RowsAffected()
	if n > 0 {
		w.log.Info("heartbeat watcher: marked inactive", zap.Int64("count", n))
	}
}
