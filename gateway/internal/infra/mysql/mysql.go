// Package mysql 在 *sql.DB 之上包一层 Agent-Mesh 默认值，并暴露 readiness
// Checker，让 /readyz 在数据库不可达时失败。
//
// 直接用 database/sql + MySQL driver（infra 层不引 ORM）。domain 包若需要
// sqlx 或 ORM 再在上面自己加。
package mysql

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/Ggrryta/agent-mesh/gateway/config"

	_ "github.com/go-sql-driver/mysql" // 注册 MySQL driver
)

// DB 是对底层 *sql.DB 的轻量包装，保留连接池设置方便日志观察。
type DB struct {
	*sql.DB
}

// Open 用 cfg 的连接池参数拨通 MySQL，并用一个带超时的 ping 验证连通性。
// 任一步失败都返回错误。
func Open(ctx context.Context, cfg *config.Config) (*DB, error) {
	if cfg.MySQLDSN == "" {
		return nil, fmt.Errorf("mysql: MYSQL_DSN is empty")
	}

	raw, err := sql.Open("mysql", cfg.MySQLDSN)
	if err != nil {
		return nil, fmt.Errorf("mysql: open: %w", err)
	}
	raw.SetMaxOpenConns(cfg.MySQLMaxOpenConns)
	raw.SetMaxIdleConns(cfg.MySQLMaxIdleConns)
	raw.SetConnMaxLifetime(cfg.MySQLConnMaxLifetime)

	pingCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if err := raw.PingContext(pingCtx); err != nil {
		_ = raw.Close()
		return nil, fmt.Errorf("mysql: ping: %w", err)
	}

	return &DB{DB: raw}, nil
}

// Checker 返回一个适合 health.Probe 的 readiness 检查器。
// 用 1s 超时卡紧，因为 /readyz 必须在压力下也能快速应答。
func (db *DB) Checker() func(ctx context.Context) error {
	return func(ctx context.Context) error {
		pingCtx, cancel := context.WithTimeout(ctx, 1*time.Second)
		defer cancel()
		return db.PingContext(pingCtx)
	}
}
