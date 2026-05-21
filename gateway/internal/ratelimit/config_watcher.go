package ratelimit

import (
	"context"
	"encoding/json"
	"strconv"

	goredis "github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

const configChannel = "config:ratelimit"

// ConfigWatcher 订阅 Redis Pub/Sub 接收限流配置变更，实时更新 RateLimiter。
//
// 下发格式（JSON）：{"requests_per_second": 100, "burst_size": 200}
//
// 下发方式：redis-cli PUBLISH config:ratelimit '{"requests_per_second":100,"burst_size":200}'
type ConfigWatcher struct {
	rdb     *goredis.Client
	limiter RateLimiter
	log     *zap.Logger
	cancel  context.CancelFunc
}

type configMessage struct {
	RequestsPerSecond int `json:"requests_per_second"`
	BurstSize         int `json:"burst_size"`
	GlobalRPS         int `json:"global_rps"`
	GlobalBurst       int `json:"global_burst"`
}

// NewConfigWatcher 创建并启动配置监听。调用 Stop() 停止。
func NewConfigWatcher(rdb *goredis.Client, limiter RateLimiter, log *zap.Logger) *ConfigWatcher {
	if log == nil {
		log = zap.NewNop()
	}
	ctx, cancel := context.WithCancel(context.Background())
	w := &ConfigWatcher{rdb: rdb, limiter: limiter, log: log, cancel: cancel}
	go w.run(ctx)
	return w
}

func (w *ConfigWatcher) run(ctx context.Context) {
	pubsub := w.rdb.Subscribe(ctx, configChannel)
	defer pubsub.Close()

	ch := pubsub.Channel()
	w.log.Info("ratelimit config watcher started", zap.String("channel", configChannel))

	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-ch:
			if !ok {
				return
			}
			w.handleMessage(msg.Payload)
		}
	}
}

func (w *ConfigWatcher) handleMessage(payload string) {
	var msg configMessage
	if err := json.Unmarshal([]byte(payload), &msg); err != nil {
		w.log.Warn("ratelimit config: invalid message", zap.String("payload", payload), zap.Error(err))
		return
	}
	if msg.RequestsPerSecond <= 0 || msg.BurstSize <= 0 {
		w.log.Warn("ratelimit config: invalid values",
			zap.String("rps", strconv.Itoa(msg.RequestsPerSecond)),
			zap.String("burst", strconv.Itoa(msg.BurstSize)))
		return
	}
	cfg := Config{
		RequestsPerSecond: msg.RequestsPerSecond,
		BurstSize:         msg.BurstSize,
		GlobalRPS:         msg.GlobalRPS,
		GlobalBurst:       msg.GlobalBurst,
	}
	w.limiter.UpdateConfig(cfg)
	w.log.Info("ratelimit config updated",
		zap.Int("requests_per_second", cfg.RequestsPerSecond),
		zap.Int("burst_size", cfg.BurstSize),
		zap.Int("global_rps", cfg.GlobalRPS),
		zap.Int("global_burst", cfg.GlobalBurst))
}

func (w *ConfigWatcher) Stop() {
	w.cancel()
}
