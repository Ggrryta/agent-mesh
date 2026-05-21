package friendship

import (
	"context"

	goredis "github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

const invalidateChannel = "friendship:invalidate"

// Invalidator 负责好友关系变更时的跨实例缓存失效。
// 写路径：变更发生时调用 Publish 广播失效通知。
// 读路径：后台订阅 channel，收到通知时清除本地缓存。
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

// Publish 广播失效通知。在 revoke/accept/reject 等变更操作后调用。
func (inv *Invalidator) Publish(ctx context.Context, agentA, agentB string) {
	key := PairKey(agentA, agentB)
	inv.cache.Invalidate(key)
	if inv.rdb == nil {
		return
	}
	if err := inv.rdb.Publish(ctx, invalidateChannel, key).Err(); err != nil {
		inv.log.Warn("friendship invalidate publish failed", zap.Error(err))
	}
}

func (inv *Invalidator) subscribe(ctx context.Context) {
	if inv.rdb == nil {
		return
	}
	pubsub := inv.rdb.Subscribe(ctx, invalidateChannel)
	defer pubsub.Close()

	ch := pubsub.Channel()
	inv.log.Info("friendship invalidator started", zap.String("channel", invalidateChannel))

	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-ch:
			if !ok {
				return
			}
			inv.cache.Invalidate(msg.Payload)
		}
	}
}

func (inv *Invalidator) Stop() {
	inv.cancel()
}
