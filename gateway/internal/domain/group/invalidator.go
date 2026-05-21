package group

import (
	"context"

	goredis "github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

const invalidateChannel = "group:invalidate"

// Invalidator 负责群组成员变更时的跨实例缓存失效。
// 群组变更粒度是 agent 级别（加人/踢人影响该 agent 的所有 pair），
// 所以广播的是 agentID 而不是 pair key。
type Invalidator struct {
	rdb    *goredis.Client
	cache  *Cache
	log    *zap.Logger
	cancel context.CancelFunc
}

func NewInvalidator(rdb *goredis.Client, cache *Cache, log *zap.Logger) *Invalidator {
	if log == nil {
		log = zap.NewNop()
	}
	ctx, cancel := context.WithCancel(context.Background())
	inv := &Invalidator{rdb: rdb, cache: cache, log: log, cancel: cancel}
	go inv.subscribe(ctx)
	return inv
}

// PublishAgent 广播某个 agent 的群组关系变更（加人/踢人后调用）。
func (inv *Invalidator) PublishAgent(ctx context.Context, agentID string) {
	inv.cache.InvalidateAgent(agentID)
	if inv.rdb == nil {
		return
	}
	if err := inv.rdb.Publish(ctx, invalidateChannel, agentID).Err(); err != nil {
		inv.log.Warn("group invalidate publish failed", zap.Error(err))
	}
}

func (inv *Invalidator) subscribe(ctx context.Context) {
	if inv.rdb == nil {
		return
	}
	pubsub := inv.rdb.Subscribe(ctx, invalidateChannel)
	defer pubsub.Close()

	ch := pubsub.Channel()
	inv.log.Info("group invalidator started", zap.String("channel", invalidateChannel))

	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-ch:
			if !ok {
				return
			}
			inv.cache.InvalidateAgent(msg.Payload)
		}
	}
}

func (inv *Invalidator) Stop() {
	inv.cancel()
}
