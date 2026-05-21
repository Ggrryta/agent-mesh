// Messaging Service: Task 状态机 + 消息投递 + Outbox → Kafka。
// 通过 gRPC 调 Identity Svc 校验权限。
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/Ggrryta/agent-mesh/gateway/config"
	"github.com/Ggrryta/agent-mesh/gateway/internal/api/admin"
	"github.com/Ggrryta/agent-mesh/gateway/internal/api/mesh"
	"github.com/Ggrryta/agent-mesh/gateway/internal/domain/friendship"
	"github.com/Ggrryta/agent-mesh/gateway/internal/domain/group"
	"github.com/Ggrryta/agent-mesh/gateway/internal/domain/inbox"
	"github.com/Ggrryta/agent-mesh/gateway/internal/domain/outbox"
	"github.com/Ggrryta/agent-mesh/gateway/internal/domain/task"
	"github.com/Ggrryta/agent-mesh/gateway/internal/feed"
	identitygrpc "github.com/Ggrryta/agent-mesh/gateway/internal/grpc"
	kafkaInfra "github.com/Ggrryta/agent-mesh/gateway/internal/infra/kafka"
	"github.com/Ggrryta/agent-mesh/gateway/internal/infra/mysql"
	"github.com/Ggrryta/agent-mesh/gateway/internal/infra/redis"
	"github.com/Ggrryta/agent-mesh/gateway/internal/middleware"
	"github.com/Ggrryta/agent-mesh/gateway/internal/observability/health"
	"github.com/Ggrryta/agent-mesh/gateway/internal/observability/logger"
	"github.com/Ggrryta/agent-mesh/gateway/pkg/auth"

	"go.uber.org/zap"
)

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
	log = log.Named("messaging-svc")
	defer log.Sync()

	probe := health.New()

	// MySQL
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	db, err := mysql.Open(ctx, cfg)
	cancel()
	if err != nil {
		log.Fatal("mysql", zap.Error(err))
	}
	defer db.Close()
	probe.AddReadinessCheck("mysql", db.Checker())

	// Auth (for JWT verification in middleware)
	signer, err := auth.NewSigner(cfg.JWTSecret, cfg.JWTTTL, cfg.JWTAgentTTL)
	if err != nil {
		log.Fatal("auth signer", zap.Error(err))
	}

	// gRPC client → Identity Svc
	identityAddr := os.Getenv("IDENTITY_GRPC_ADDR")
	if identityAddr == "" {
		identityAddr = "127.0.0.1:50051"
	}
	identityClient, err := identitygrpc.NewIdentityClient(identityAddr)
	if err != nil {
		log.Fatal("identity grpc client", zap.String("addr", identityAddr), zap.Error(err))
	}
	defer identityClient.Close()
	log.Info("connected to Identity Svc", zap.String("addr", identityAddr))

	// Domain services
	taskRepo := task.NewSQLRepo(db.DB)
	// identityClient 实现了 task.AgentLookup 和 task.FriendshipChecker
	taskSvc := task.NewService(taskRepo, identityClient, identityClient)

	inboxRepo := inbox.NewSQLRepo(db.DB)
	inboxSvc := inbox.NewService(inboxRepo)
	taskSvc.WithInbox(inboxSvc)

	// Groups + Friendship
	friendRepo := friendship.NewSQLRepo(db.DB)
	friendSvc := friendship.NewService(friendRepo, identityClient)
	groupRepo := group.NewSQLRepo(db.DB)
	groupSvc := group.NewService(groupRepo, log.Named("group"))
	groupSvc.WithEligibilityCheck(identityClient, friendSvc)
	taskSvc.WithGroups(groupSvc)

	// FeedHub：通过 Redis Pub/Sub + Kafka 推事件给 Push Gateway
	var feedHub *feed.Hub
	if cfg.RedisAddr != "" {
		rdb, err := redis.Open(context.Background(), cfg)
		if err == nil {
			feedHub = feed.NewHub(rdb, log.Named("feed"))
		}
	}
	if feedHub == nil {
		feedHub = feed.NewHub(nil, log.Named("feed"))
	}
	// Kafka feed 广播（Push Gateway 从 feed.realtime topic 消费）
	if cfg.KafkaBrokers != "" {
		feedKafka := kafkaInfra.NewProducer(cfg.KafkaBrokers, log.Named("feed-kafka"))
		feedHub.WithKafka(feedKafka)
		log.Info("feed: kafka broadcast enabled")
	}

	// Outbox + Kafka
	outboxRepo := outbox.NewSQLRepo(db.DB)
	if cfg.KafkaBrokers != "" {
		taskSvc.WithOutbox(outboxRepo.AsTaskOutboxWriter())

		kafkaProd := kafkaInfra.NewProducer(cfg.KafkaBrokers, log.Named("kafka"))
		// 乐观直发：写完 outbox 后立即尝试发 Kafka，Dispatcher 兜底
		taskSvc.WithKafka(kafkaProd)
		dispatcher := outbox.NewDispatcher(outboxRepo, func(ctx context.Context, event *outbox.Event) error {
			parts := strings.SplitN(event.EventType, ":", 2)
			key := ""
			if len(parts) == 2 {
				key = parts[1]
			}
			return kafkaProd.Publish(ctx, "inbox.events", key, event.Payload)
		}, log.Named("outbox-dispatcher"))

		// 死信通知：投递失败时通知发送方 agent
		dispatcher.WithDeadLetterNotifier(func(ctx context.Context, event *outbox.Event, errMsg string) {
			// event_type 格式："inbox.message:{toAgentID}"，从中提取发送方信息
			// payload 里有 from_agent_id
			var payload struct {
				AgentID string `json:"agent_id"` // 目标 agent（投递失败的对象）
				TaskID  string `json:"task_id"`
			}
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				return
			}
			// 通知目标 agent 的 owner（通过 feed 推前端）
			if feedHub != nil {
				ownerUID, _, found := identityClient.Lookup(ctx, payload.AgentID)
				if found {
					feedHub.Publish(ctx, ownerUID, &feed.FeedEvent{
						Type:    "delivery_failure",
						AgentID: payload.AgentID,
						TaskID:  payload.TaskID,
						Payload: json.RawMessage(fmt.Sprintf(`{"error":%q,"event_type":%q}`, errMsg, event.EventType)),
					})
				}
			}
			log.Error("dead letter: notified owner",
				zap.String("target_agent", payload.AgentID),
				zap.String("task_id", payload.TaskID),
				zap.String("error", errMsg))
		})

		// Kafka 双写（inbox 表也写，作为 fallback）
		if cfg.KafkaBrokers != "" {
			inboxSvc.WithKafka(kafkaProd)
		}

		bgCtx, cancelBG := context.WithCancel(context.Background())
		defer cancelBG()
		go dispatcher.Run(bgCtx)
		log.Info("outbox dispatcher started")

		// Auto-close chatter tasks
		go func() {
			ticker := time.NewTicker(30 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-bgCtx.Done():
					return
				case <-ticker.C:
					n, _ := taskSvc.AutoCloseChatterTasks(bgCtx, 5, 2*time.Minute)
					if n > 0 {
						log.Info("auto-closed chatter tasks", zap.Int("count", n))
					}
				}
			}
		}()
	}

	// HTTP handler (mesh API + admin task API)
	meshH := mesh.New(nil, nil, taskSvc, inboxSvc, signer,
		mesh.WithGroups(groupSvc),
		mesh.WithFriends(friendSvc),
		mesh.WithFeed(feedHub),
	)

	// admin handler：Messaging Svc 只处理 task/friends/groups 路由。
	// agents/users/keys/skills 传 nil——这些路由由 API Gateway 路由到 Identity Svc，不会到这里。
	adminH := admin.New(nil, nil, nil, nil, friendSvc, signer,
		admin.WithTasks(taskSvc),
		admin.WithGroups(groupSvc),
	)
	_ = friendSvc

	httpAddr := cfg.BusinessAddr
	httpMux := http.NewServeMux()
	httpMux.HandleFunc("/healthz", probe.LivenessHandler())
	httpMux.HandleFunc("/readyz", probe.ReadinessHandler())
	httpMux.Handle("/v1/mesh/", http.StripPrefix("/v1/mesh", meshH.Mux()))
	httpMux.Handle("/v1/admin/", http.StripPrefix("/v1/admin", adminH.Mux()))

	// Dead letter admin endpoints
	httpMux.HandleFunc("/v1/admin/dead-letters", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		dls, err := outboxRepo.ListDeadLetters(r.Context(), 50)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"dead_letters": dls, "count": len(dls)})
	})
	httpMux.HandleFunc("/v1/admin/dead-letters/retry", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			ID int64 `json:"id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ID == 0 {
			http.Error(w, "id required", http.StatusBadRequest)
			return
		}
		if err := outboxRepo.RetryDeadLetter(r.Context(), req.ID); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "retried"})
	})

	httpHandler := middleware.WithRequestID(
		middleware.Recover(log)(
			middleware.Metrics(
				middleware.AccessLog(log)(httpMux),
			),
		),
	)

	httpSrv := &http.Server{
		Addr:         httpAddr,
		Handler:      httpHandler,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 60 * time.Second,
		IdleTimeout:  120 * time.Second,
	}
	go func() {
		log.Info("HTTP server started", zap.String("addr", httpAddr))
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal("http", zap.Error(err))
		}
	}()

	// Shutdown
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Info("shutting down")

	shutCtx, shutCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutCancel()
	httpSrv.Shutdown(shutCtx)
	log.Info("messaging-svc stopped")
}
