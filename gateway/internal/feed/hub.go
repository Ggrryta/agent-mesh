package feed

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/Ggrryta/agent-mesh/gateway/internal/observability/metrics"

	goredis "github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

// KafkaPublisher 是 Kafka 生产者接口（避免直接依赖 infra/kafka 包）。
type KafkaPublisher interface {
	Publish(ctx context.Context, topic, key string, value []byte) error
}

const (
	channelPrefix  = "feed:user:"
	subscriberBuf  = 64
	publishTimeout = 2 * time.Second
)

// FeedEvent 是推给前端 WebSocket 的事件。
type FeedEvent struct {
	EventID   string          `json:"event_id"`
	Type      string          `json:"type"`
	AgentID   string          `json:"agent_id"`
	TaskID    string          `json:"task_id"`
	Payload   json.RawMessage `json:"payload"`
	Timestamp time.Time       `json:"timestamp"`
}

// Subscriber 代表一个前端 WebSocket 连接的订阅。
type Subscriber struct {
	UID  int64
	Ch   chan *FeedEvent
	done chan struct{}
}

// Done 返回一个在 Unsubscribe 后关闭的 channel。
func (s *Subscriber) Done() <-chan struct{} { return s.done }

// Hub 管理前端用户的实时事件订阅。
// 通过 Redis Pub/Sub 或 Kafka 实现跨 Pod 广播。
type Hub struct {
	rdb    *goredis.Client
	kafka  KafkaPublisher // 可为 nil
	mu     sync.RWMutex
	subs   map[int64][]*Subscriber
	recent *RecentBuffer
	log    *zap.Logger
}

// NewHub 构造 FeedHub。rdb 可为 nil（降级为本地模式，不跨 Pod）。
func NewHub(rdb *goredis.Client, log *zap.Logger) *Hub {
	return &Hub{
		rdb:    rdb,
		subs:   make(map[int64][]*Subscriber),
		recent: NewRecentBuffer(),
		log:    log,
	}
}

// WithKafka 注入 Kafka producer，启用 feed 事件的 Kafka 广播。
func (h *Hub) WithKafka(k KafkaPublisher) { h.kafka = k }

// Subscribe 注册一个订阅者，返回 Subscriber 用于接收事件。
func (h *Hub) Subscribe(uid int64) *Subscriber {
	sub := &Subscriber{
		UID:  uid,
		Ch:   make(chan *FeedEvent, subscriberBuf),
		done: make(chan struct{}),
	}
	h.mu.Lock()
	h.subs[uid] = append(h.subs[uid], sub)
	h.mu.Unlock()
	metrics.FeedActiveSubscribers.Inc()
	return sub
}

// Unsubscribe 移除订阅者并关闭其 Done channel。
func (h *Hub) Unsubscribe(sub *Subscriber) {
	h.mu.Lock()
	defer h.mu.Unlock()
	list := h.subs[sub.UID]
	for i, s := range list {
		if s == sub {
			h.subs[sub.UID] = append(list[:i], list[i+1:]...)
			break
		}
	}
	if len(h.subs[sub.UID]) == 0 {
		delete(h.subs, sub.UID)
	}
	select {
	case <-sub.done:
	default:
		close(sub.done)
	}
	metrics.FeedActiveSubscribers.Dec()
}

// Publish 向指定用户的所有订阅者推送事件。
// 同时通过 Redis Pub/Sub 和/或 Kafka 广播给其它 Pod。
func (h *Hub) Publish(ctx context.Context, ownerUID int64, event *FeedEvent) {
	if event == nil {
		return
	}
	if event.EventID == "" {
		event.EventID = generateEventID()
	}
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now()
	}
	metrics.FeedPublishTotal.Inc()

	// 本地投递
	h.DeliverLocal(ownerUID, event)

	data, err := json.Marshal(event)
	if err != nil {
		h.log.Warn("feed: marshal event", zap.Error(err))
		return
	}

	// Redis 广播（跨 Pod）
	if h.rdb != nil {
		pubCtx, cancel := context.WithTimeout(ctx, publishTimeout)
		defer cancel()
		channel := fmt.Sprintf("%s%d", channelPrefix, ownerUID)
		if err := h.rdb.Publish(pubCtx, channel, data).Err(); err != nil {
			h.log.Warn("feed: redis publish", zap.Error(err), zap.Int64("uid", ownerUID))
		}
	}

	// Kafka 广播（Push Gateway 消费）
	if h.kafka != nil {
		key := strconv.FormatInt(ownerUID, 10)
		if err := h.kafka.Publish(ctx, "feed.realtime", key, data); err != nil {
			h.log.Warn("feed: kafka publish", zap.Error(err), zap.Int64("uid", ownerUID))
		}
	}
}

// DeliverLocal 向本 Pod 内该 uid 的所有 subscriber 投递事件。
// 由 Kafka consumer 或 Redis subscriber 调用。
func (h *Hub) DeliverLocal(uid int64, event *FeedEvent) {
	h.recent.Append(uid, event)

	h.mu.RLock()
	list := h.subs[uid]
	h.mu.RUnlock()

	for _, sub := range list {
		select {
		case sub.Ch <- event:
		default:
			h.log.Debug("feed: subscriber channel full, dropping", zap.Int64("uid", uid))
		}
	}
}

// Replay 返回 lastEventID 之后的所有缓存事件，供断线重连时补推。
func (h *Hub) Replay(uid int64, lastEventID string) []*FeedEvent {
	return h.recent.Since(uid, lastEventID)
}

// Run 启动 Redis Pub/Sub 订阅循环，接收其它 Pod 广播的事件并投递给本地订阅者。
// 阻塞直到 ctx 取消。rdb 为 nil 时立即返回。
func (h *Hub) Run(ctx context.Context) {
	if h.rdb == nil {
		h.log.Info("feed: no redis, running in local-only mode")
		<-ctx.Done()
		return
	}

	pubsub := h.rdb.PSubscribe(ctx, channelPrefix+"*")
	defer pubsub.Close()

	h.log.Info("feed: redis pub/sub subscriber started", zap.String("pattern", channelPrefix+"*"))

	ch := pubsub.Channel()
	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-ch:
			if !ok {
				return
			}
			h.handleRedisMessage(msg)
		}
	}
}

func (h *Hub) handleRedisMessage(msg *goredis.Message) {
	// channel 格式: "feed:user:12345"
	if len(msg.Channel) <= len(channelPrefix) {
		return
	}
	uidStr := msg.Channel[len(channelPrefix):]
	var uid int64
	for _, c := range uidStr {
		if c < '0' || c > '9' {
			return
		}
		uid = uid*10 + int64(c-'0')
	}

	var event FeedEvent
	if err := json.Unmarshal([]byte(msg.Payload), &event); err != nil {
		h.log.Warn("feed: unmarshal redis message", zap.Error(err))
		return
	}
	h.DeliverLocal(uid, &event)
}

// ActiveSubscribers 返回当前活跃订阅者数量（用于监控）。
func (h *Hub) ActiveSubscribers() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	count := 0
	for _, list := range h.subs {
		count += len(list)
	}
	return count
}

func generateEventID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return fmt.Sprintf("%d-%s", time.Now().UnixMilli(), hex.EncodeToString(b))
}
