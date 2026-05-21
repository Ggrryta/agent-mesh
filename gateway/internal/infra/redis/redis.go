// Package redis 在 go-redis 之上包一层 Agent-Mesh 默认值，并暴露 readiness
// Checker。
//
// 每个进程保持一个 *redis.Client。将来要上分片 / cluster，换这个构造器即可。
package redis

import (
	"context"
	"fmt"
	"time"

	"github.com/Ggrryta/agent-mesh/gateway/config"

	goredis "github.com/redis/go-redis/v9"
)

// Client 是一个别名，让调用方只需从本包引一个符号，不必混用 goredis 和
// 我们的包装。
type Client = goredis.Client

// Open 构造 go-redis 客户端，用 PING 验证连通性，成功后返回。
func Open(ctx context.Context, cfg *config.Config) (*Client, error) {
	if cfg.RedisAddr == "" {
		return nil, fmt.Errorf("redis: REDIS_ADDR is empty")
	}

	c := goredis.NewClient(&goredis.Options{
		Addr:         cfg.RedisAddr,
		Password:     cfg.RedisPassword,
		DB:           cfg.RedisDB,
		DialTimeout:  3 * time.Second,
		ReadTimeout:  2 * time.Second,
		WriteTimeout: 2 * time.Second,
		PoolSize:     50,
		MinIdleConns: 5,
	})

	pingCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if err := c.Ping(pingCtx).Err(); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("redis: ping: %w", err)
	}
	return c, nil
}

// Checker 返回一个适合 health.Probe 的 readiness 检查器。
func Checker(c *Client) func(ctx context.Context) error {
	return func(ctx context.Context) error {
		pingCtx, cancel := context.WithTimeout(ctx, 1*time.Second)
		defer cancel()
		return c.Ping(pingCtx).Err()
	}
}
