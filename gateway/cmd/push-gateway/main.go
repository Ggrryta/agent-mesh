// Push Gateway: WebSocket 实时推送。从 Kafka feed.realtime topic 消费事件推给浏览器。
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Ggrryta/agent-mesh/gateway/config"
	"github.com/Ggrryta/agent-mesh/gateway/internal/feed"
	"github.com/Ggrryta/agent-mesh/gateway/internal/infra/redis"
	"github.com/Ggrryta/agent-mesh/gateway/internal/middleware"
	"github.com/Ggrryta/agent-mesh/gateway/internal/observability/health"
	"github.com/Ggrryta/agent-mesh/gateway/internal/observability/logger"

	"github.com/gorilla/websocket"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	kafkago "github.com/segmentio/kafka-go"
	"go.uber.org/zap"
)

var kafkaConsumeLag = promauto.NewHistogram(prometheus.HistogramOpts{
	Namespace: "mesh",
	Subsystem: "push_gateway",
	Name:      "kafka_consume_lag_seconds",
	Help:      "Lag between message production and consumption.",
	Buckets:   []float64{0.01, 0.05, 0.1, 0.5, 1, 5, 10},
})

func main() {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "config: %v\n", err)
		os.Exit(1)
	}

	log, err := logger.New(cfg.LogLevel, cfg.LogFormat, cfg.PodName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "logger: %v\n", err)
		os.Exit(1)
	}
	log = log.Named("push-gateway")
	defer log.Sync()

	probe := health.New()

	// Redis (for cross-Pod Pub/Sub)
	var rdbClient *redis.Client
	if cfg.RedisAddr != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		rdbClient, err = redis.Open(ctx, cfg)
		cancel()
		if err != nil {
			log.Warn("redis unavailable", zap.Error(err))
		}
	}

	// FeedHub
	feedHub := feed.NewHub(rdbClient, log.Named("feed"))
	bgCtx, cancelBG := context.WithCancel(context.Background())
	defer cancelBG()
	if rdbClient != nil {
		go feedHub.Run(bgCtx)
	}

	// HTTP (WebSocket endpoint)
	httpAddr := cfg.BusinessAddr
	httpMux := http.NewServeMux()
	httpMux.HandleFunc("/healthz", probe.LivenessHandler())
	httpMux.HandleFunc("/readyz", probe.ReadinessHandler())

	// WebSocket feed endpoint
	wsUpgrader := websocket.Upgrader{
		ReadBufferSize:  1024,
		WriteBufferSize: 4096,
		CheckOrigin:     func(r *http.Request) bool { return true },
	}
	httpMux.Handle("/v1/admin/ws/feed", middleware.TrustGateway(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		uid, ok := middleware.UIDFromContext(r.Context())
		if !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		conn, err := wsUpgrader.Upgrade(w, r, nil)
		if err != nil {
			log.Warn("ws upgrade failed", zap.Error(err))
			return
		}
		defer conn.Close()

		// 断线重连补推：客户端通过 query param 传上次收到的最后一个 event_id
		lastEventID := r.URL.Query().Get("last_event_id")
		if lastEventID != "" {
			missed := feedHub.Replay(uid, lastEventID)
			for _, event := range missed {
				conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
				if err := conn.WriteJSON(event); err != nil {
					return
				}
			}
		}

		sub := feedHub.Subscribe(uid)
		defer feedHub.Unsubscribe(sub)

		// Read goroutine (handle close/pong)
		go func() {
			defer conn.Close()
			conn.SetReadLimit(512)
			for {
				if _, _, err := conn.NextReader(); err != nil {
					break
				}
			}
		}()

		// Write loop
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case event, ok := <-sub.Ch:
				if !ok {
					return
				}
				conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
				if err := conn.WriteJSON(event); err != nil {
					return
				}
			case <-ticker.C:
				conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
				if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
					return
				}
			case <-sub.Done():
				return
			}
		}
	})))

	httpHandler := middleware.CORS(nil)(
		middleware.WithRequestID(
			middleware.Recover(log)(
				middleware.Metrics(
					middleware.AccessLog(log)(httpMux),
				),
			),
		),
	)

	httpSrv := &http.Server{
		Addr:         httpAddr,
		Handler:      httpHandler,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 120 * time.Second, // WebSocket 需要更长写超时
		IdleTimeout:  300 * time.Second,
	}
	go func() {
		log.Info("HTTP server started", zap.String("addr", httpAddr))
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal("http", zap.Error(err))
		}
	}()

	// Kafka consumer for feed.realtime topic（替代 Redis Pub/Sub 的跨 Pod 方案）
	if cfg.KafkaBrokers != "" {
		go runFeedKafkaConsumer(bgCtx, cfg.KafkaBrokers, cfg.PodName, feedHub, log)
		log.Info("kafka feed consumer started", zap.String("brokers", cfg.KafkaBrokers))
	}

	// Shutdown
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Info("shutting down")

	shutCtx, shutCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutCancel()
	httpSrv.Shutdown(shutCtx)
	log.Info("push-gateway stopped")
}

// runFeedKafkaConsumer 从 Kafka feed.realtime topic 消费事件，投递到本地 feedHub。
// 每个实例用独立 GroupID（广播模式），确保所有实例都收到所有消息。
func runFeedKafkaConsumer(ctx context.Context, brokers, podName string, hub *feed.Hub, log *zap.Logger) {
	addrs := strings.Split(brokers, ",")
	groupID := "push-gateway-" + podName
	reader := kafkago.NewReader(kafkago.ReaderConfig{
		Brokers:        addrs,
		Topic:          "feed.realtime",
		GroupID:        groupID,
		MinBytes:       1,
		MaxBytes:       1e6,
		CommitInterval: time.Second,
	})
	defer reader.Close()

	log.Info("kafka feed consumer loop started", zap.String("group_id", groupID))

	backoff := time.Second
	const maxBackoff = 30 * time.Second

	for {
		msg, err := reader.ReadMessage(ctx)
		if err != nil {
			if ctx.Err() != nil {
				log.Info("kafka feed consumer shutting down")
				return
			}
			log.Warn("kafka feed read error", zap.Error(err), zap.Duration("backoff", backoff))
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return
			}
			backoff = min(backoff*2, maxBackoff)
			continue
		}
		backoff = time.Second

		// 消费延迟监控
		if !msg.Time.IsZero() {
			lag := time.Since(msg.Time)
			kafkaConsumeLag.Observe(lag.Seconds())
		}

		// key = uid (string)
		uid, err := strconv.ParseInt(string(msg.Key), 10, 64)
		if err != nil {
			log.Warn("kafka feed: invalid uid key", zap.String("key", string(msg.Key)))
			continue
		}

		// value = JSON(FeedEvent)
		var event feed.FeedEvent
		if err := json.Unmarshal(msg.Value, &event); err != nil {
			log.Warn("kafka feed: unmarshal error", zap.Error(err))
			continue
		}

		hub.DeliverLocal(uid, &event)
	}
}
