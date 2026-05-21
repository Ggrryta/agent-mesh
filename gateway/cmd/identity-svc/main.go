// Identity Service: 用户/Agent/好友/群组/Market 管理。
// 暴露 HTTP（admin API）+ gRPC（供 Messaging Svc 调用）。
package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Ggrryta/agent-mesh/gateway/config"
	"github.com/Ggrryta/agent-mesh/gateway/internal/api/admin"
	"github.com/Ggrryta/agent-mesh/gateway/internal/api/mesh"
	"github.com/Ggrryta/agent-mesh/gateway/internal/domain/agent"
	"github.com/Ggrryta/agent-mesh/gateway/internal/domain/apikey"
	"github.com/Ggrryta/agent-mesh/gateway/internal/domain/friendship"
	"github.com/Ggrryta/agent-mesh/gateway/internal/domain/group"
	"github.com/Ggrryta/agent-mesh/gateway/internal/domain/prober"
	"github.com/Ggrryta/agent-mesh/gateway/internal/domain/skill"
	"github.com/Ggrryta/agent-mesh/gateway/internal/domain/user"
	identitygrpc "github.com/Ggrryta/agent-mesh/gateway/internal/grpc"
	"github.com/Ggrryta/agent-mesh/gateway/internal/infra/mysql"
	"github.com/Ggrryta/agent-mesh/gateway/internal/infra/redis"
	"github.com/Ggrryta/agent-mesh/gateway/internal/middleware"
	"github.com/Ggrryta/agent-mesh/gateway/internal/observability/health"
	"github.com/Ggrryta/agent-mesh/gateway/internal/observability/logger"
	"github.com/Ggrryta/agent-mesh/gateway/pkg/auth"

	grpclib "google.golang.org/grpc"
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
	log = log.Named("identity-svc")
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

	// Auth
	signer, err := auth.NewSigner(cfg.JWTSecret, cfg.JWTTTL, cfg.JWTAgentTTL)
	if err != nil {
		log.Fatal("auth signer", zap.Error(err))
	}

	// Domain services
	userSvc := user.NewService(user.NewSQLRepo(db.DB))
	agentSvc := agent.NewService(agent.NewSQLRepo(db.DB), agent.NewCache())
	apiKeySvc := apikey.NewService(apikey.NewSQLRepo(db.DB), log.Named("apikey"))
	skillRepo := skill.NewSQLRepo(db.DB)
	friendSvc := friendship.NewService(friendship.NewSQLRepo(db.DB), agent.NewLookupAdapter(agentSvc))

	// 好友关系缓存（可选：有 Redis 时启用 Pub/Sub 跨实例失效）
	friendCache := friendship.NewCache(0)
	if cfg.RedisAddr != "" {
		rdbCtx, rdbCancel := context.WithTimeout(context.Background(), 5*time.Second)
		rdb, err := redis.Open(rdbCtx, cfg)
		rdbCancel()
		if err != nil {
			log.Warn("redis unavailable, friendship cache without pub/sub", zap.Error(err))
			friendSvc.WithCache(friendCache, nil)
		} else {
			inv := friendship.NewInvalidator(rdb, friendCache, log.Named("friendship-inv"))
			friendSvc.WithCache(friendCache, inv)
			log.Info("friendship cache with pub/sub invalidation enabled")
		}
	} else {
		friendSvc.WithCache(friendCache, nil)
		log.Info("friendship cache enabled (local only, no pub/sub)")
	}
	groupRepo := group.NewSQLRepo(db.DB)
	groupCache := group.NewCache(0)
	groupSvc := group.NewService(groupRepo, log.Named("group")).
		WithEligibilityCheck(agent.NewLookupAdapter(agentSvc), friendSvc)
	if cfg.RedisAddr != "" {
		// Redis 已在 friendship 缓存阶段连接过，复用同一个连接思路：
		// 这里简单重新 Open 一次（go-redis 内部有连接池，实际共享底层 TCP）
		rdbCtx2, rdbCancel2 := context.WithTimeout(context.Background(), 5*time.Second)
		rdb2, err := redis.Open(rdbCtx2, cfg)
		rdbCancel2()
		if err == nil {
			groupInv := group.NewInvalidator(rdb2, groupCache, log.Named("group-inv"))
			groupSvc.WithCache(groupCache, groupInv)
			log.Info("group cache with pub/sub invalidation enabled")
		} else {
			groupSvc.WithCache(groupCache, nil)
		}
	} else {
		groupSvc.WithCache(groupCache, nil)
		log.Info("group cache enabled (local only)")
	}
	// HTTP handler (admin API)
	adminH := admin.New(userSvc, agentSvc, skillRepo, apiKeySvc, friendSvc, signer,
		admin.WithGroups(groupSvc),
	)

	httpAddr := cfg.BusinessAddr // default :8080
	httpMux := http.NewServeMux()
	httpMux.HandleFunc("/healthz", probe.LivenessHandler())
	httpMux.HandleFunc("/readyz", probe.ReadinessHandler())
	httpMux.Handle("/v1/admin/", http.StripPrefix("/v1/admin", adminH.Mux()))

	// Mesh API（agent auth + heartbeat + friends/groups）
	meshH := mesh.New(agentSvc, apiKeySvc, nil, nil, signer,
		mesh.WithFriends(friendSvc),
		mesh.WithGroups(groupSvc),
	)
	httpMux.Handle("/v1/mesh/", http.StripPrefix("/v1/mesh", meshH.Mux()))

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

	// gRPC server
	grpcAddr := ":50051"
	if v := os.Getenv("GRPC_ADDR"); v != "" {
		grpcAddr = v
	}
	grpcSrv := grpclib.NewServer()
	identitygrpc.NewIdentityServer(agentSvc, friendSvc, groupSvc).Register(grpcSrv)

	// Start
	go func() {
		lis, err := net.Listen("tcp", grpcAddr)
		if err != nil {
			log.Fatal("grpc listen", zap.Error(err))
		}
		log.Info("gRPC server started", zap.String("addr", grpcAddr))
		if err := grpcSrv.Serve(lis); err != nil {
			log.Error("grpc serve", zap.Error(err))
		}
	}()

	go func() {
		log.Info("HTTP server started", zap.String("addr", httpAddr))
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal("http", zap.Error(err))
		}
	}()

	// Agent health prober
	proberCtx, proberCancel := context.WithCancel(context.Background())
	defer proberCancel()
	prb := prober.New(db.DB, agentSvc, prober.Config{}, log.Named("prober"))
	go prb.Run(proberCtx)
	log.Info("agent prober started")

	// Heartbeat timeout watcher
	hbw := prober.NewHeartbeatWatcher(db.DB, prober.HeartbeatWatcherConfig{}, log.Named("heartbeat-watcher"))
	go hbw.Run(proberCtx)
	log.Info("heartbeat watcher started")

	// Shutdown
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Info("shutting down")

	shutCtx, shutCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutCancel()
	httpSrv.Shutdown(shutCtx)
	grpcSrv.GracefulStop()
	log.Info("identity-svc stopped")
}
