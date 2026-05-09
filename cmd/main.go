package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"agent-gateway/config"
	"agent-gateway/internal/handler"
	"agent-gateway/internal/middleware"
	"agent-gateway/internal/model"
	"agent-gateway/internal/repo"
	"agent-gateway/internal/service"
	"agent-gateway/pkg/circuitbreaker"
	"agent-gateway/pkg/discovery"
	"agent-gateway/pkg/logger"
	"agent-gateway/pkg/metrics"
	"agent-gateway/pkg/ratelimit"
	"agent-gateway/pkg/tracer"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/hlog"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

var (
	configPath = flag.String("config", "config/config.yaml", "配置文件路径")
)

func main() {
	flag.Parse()

	// 1. 加载配置
	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Printf("load config failed: %v\n", err)
		os.Exit(1)
	}

	// 2. 初始化日志
	if err := logger.Init(cfg.Log.Level, cfg.Log.Format); err != nil {
		fmt.Printf("init logger failed: %v\n", err)
		os.Exit(1)
	}
	defer logger.Sync()

	// 初始化 OpenTelemetry
	shutdownTracer, err := tracer.Init(context.Background(), cfg.Telemetry.ServiceName, cfg.Telemetry.OTLPEndpoint, cfg.Telemetry.SampleRate)
	if err != nil {
		logger.Fatal("init tracer failed", zap.Error(err))
	}
	defer shutdownTracer(context.Background())

	logger.Info("config loaded",
		zap.Int("port", cfg.Server.Port),
		zap.String("mode", cfg.Server.Mode),
	)

	// 3. 初始化 MySQL
	if cfg.Database.Host == "" {
		logger.Fatal("database host is required")
	}
	db, err := gorm.Open(mysql.Open(cfg.Database.DSN()), &gorm.Config{})
	if err != nil {
		logger.Fatal("init mysql failed", zap.Error(err))
	}
	sqlDB, _ := db.DB()
	sqlDB.SetMaxIdleConns(cfg.Database.MaxIdleConns)
	sqlDB.SetMaxOpenConns(cfg.Database.MaxOpenConns)
	logger.Info("mysql connected",
		zap.String("host", cfg.Database.Host),
		zap.Int("port", cfg.Database.Port),
	)

	// 4. 初始化 Redis
	if cfg.Redis.Host == "" {
		logger.Fatal("redis host is required")
	}
	rdb := redis.NewClient(&redis.Options{
		Addr:     cfg.Redis.Addr(),
		Password: cfg.Redis.Password,
		DB:       cfg.Redis.DB,
		PoolSize: cfg.Redis.PoolSize,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := rdb.Ping(ctx).Err(); err != nil {
		logger.Fatal("init redis failed", zap.Error(err))
	}
	logger.Info("redis connected",
		zap.String("addr", cfg.Redis.Addr()),
	)

	// 5. 依赖注入
	consumerRepo := repo.NewConsumerRepo(db)
	taskRepo := repo.NewAsyncTaskRepo(rdb)
	reliableTaskRepo := repo.NewReliableAsyncTaskRepo(db)
	outboxRepo := repo.NewOutboxRepo(db)
	apiKeyRepo := repo.NewAPIKeyRepo(db)
	agentRepo := repo.NewAgentRepo(db)
	agentSkillRepo := repo.NewAgentSkillRepo(db)
	agentPermRepo := repo.NewAgentPermissionRepo(db, rdb)
	agentApplyRepo := repo.NewAgentApplyRepo(db, rdb)
	configRepo := repo.NewConfigRepo(db)
	friendshipRepo := repo.NewFriendshipRepo(db)
	taskV2Repo := repo.NewTaskV2Repo(db)

	// 5.1 限流器
	limiter := ratelimit.NewLimiter(rdb, ratelimit.Config{
		Enabled:          true,
		FallbackStrategy: ratelimit.FallbackLocal,
		FailureThreshold: 5,
		RecoveryTimeout:  30 * time.Second,
	})

	// 5.2 Nacos 配置热更新
	nacosClient, err := config.NewNacosClient(cfg.Nacos)
	if err != nil {
		logger.Warn("init nacos client failed, falling back to redis pub/sub", zap.Error(err))
		nacosClient = nil
	}

	var gatewayHandler *handler.GatewayHandler
	onConfigChange := func(cfgType model.ConfigType, _ string) {
		if cfgType == model.ConfigTypeCircuitBreaker && gatewayHandler != nil {
			gatewayHandler.OnCircuitBreakerConfigChange()
		}
		logger.Info("config reloaded", zap.String("config_type", string(cfgType)))
	}

	var nacosWatcher *config.NacosConfigWatcher
	var instanceWatcher *discovery.InstanceWatcher
	var instanceIP string
	if nacosClient != nil {
		canaryCfg := config.GetCanaryConfig(cfg.Nacos)
		nacosWatcher = config.NewNacosConfigWatcher(nacosClient, cfg.Nacos.Group, canaryCfg)
		nacosWatcher.OnConfigChange(onConfigChange)
		if err := nacosWatcher.Start(); err != nil {
			logger.Fatal("start nacos config watcher failed", zap.Error(err))
		}
		defer nacosWatcher.Stop()

		// 5.3 实例感知：通过 Nacos 服务发现动态调整本地配额比例
		instanceIP = resolveInstanceIP()
		instanceWatcher = discovery.New(
			nacosClient.NamingClient(),
			cfg.Telemetry.ServiceName,
			instanceIP,
			uint64(cfg.Server.Port),
			cfg.Nacos.Group,
		)
		instanceWatcher.OnChange(func(count int) {
			ratio := 1.0 / float64(count)
			limiter.SetLocalRatio(ratio)
			logger.Info("instance count changed, local ratio updated",
				zap.Int("instance_count", count),
				zap.Float64("local_ratio", ratio),
			)
		})
		if err := instanceWatcher.Start(context.Background()); err != nil {
			logger.Warn("start instance watcher failed, using default local ratio", zap.Error(err))
		} else {
			defer instanceWatcher.Stop()
		}
	} else {
		fallbackWatcher := config.NewConfigWatcher(rdb, configRepo)
		fallbackWatcher.OnConfigChange(onConfigChange)
		if err := fallbackWatcher.Start(context.Background()); err != nil {
			logger.Fatal("start config watcher failed", zap.Error(err))
		}
		defer fallbackWatcher.Stop()
	}

	// 5.4 Agent 注册中台
	var agentNotifier service.AgentRegistryNotifier
	if nacosClient != nil {
		registryService := cfg.Nacos.AgentRegistryService
		if registryService == "" {
			registryService = "agent-registry"
		}
		agentNotifier = service.NewNacosAgentRegistryNotifier(
			nacosClient.NamingClient(),
			registryService,
			cfg.Nacos.Group,
		)
	} else {
		agentNotifier = service.NewNoopAgentRegistryNotifier()
	}

	agentCache := service.NewAgentCache(agentRepo)
	agentCache.SetNotifier(agentNotifier)
	if err := agentCache.Start(context.Background()); err != nil {
		logger.Fatal("start agent cache failed", zap.Error(err))
	}
	defer agentCache.Stop()

	agentSvc := service.NewAgentService(agentRepo, agentSkillRepo, agentCache, agentNotifier)

	// 5.5 A2A 调用器
	a2aClientPool := service.NewA2AClientPool()
	a2aClientPool.StartCleanup()
	defer a2aClientPool.Stop()
	a2aInvoker := service.NewA2AInvoker(a2aClientPool)
	breakerFactory := circuitbreaker.NewBreakerFactory(rdb)
	agentCallGuard := service.NewAgentCallGuard(breakerFactory)

	// 5.5.1 GAS 通信层:在线状态 + Inbox SSE + 消息分发
	onlineRegistry := service.NewOnlineRegistry(rdb, 90*time.Second)
	inboxHub := service.NewInboxHub()
	inboxHub.StartPingLoop(context.Background(), 30*time.Second)

	// MonitorHub: 独立于 InboxHub 的监控流(给 Web UI 订阅)
	monitorHub := service.NewMonitorHub()
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			monitorHub.BroadcastPing()
		}
	}()

	agentDispatcher := service.NewAgentDispatcher(agentRepo, friendshipRepo, taskV2Repo, onlineRegistry, inboxHub, a2aInvoker)
	agentDispatcher.SetMonitorHub(monitorHub)
	_ = agentDispatcher // 已供 TaskV2Handler 使用

	// 5.6 异步任务 Worker
	taskWorker := service.NewTaskWorker(taskRepo, agentCache, a2aInvoker, agentCallGuard)
	taskWorker.Start()

	// 5.6.1 可靠异步任务链路：DB 事实源 + Outbox/MQ 投递 + Redis 查询缓存。
	taskPublisher, taskEvents, closeTaskMQ := initReliableTaskMQ(cfg.AsyncMQ)
	defer closeTaskMQ()
	outboxDispatcher := service.NewOutboxDispatcher(outboxRepo, taskPublisher)
	outboxDispatcher.Start()
	defer outboxDispatcher.Stop()
	reliableTaskWorker := service.NewReliableTaskWorker(reliableTaskRepo, taskRepo, agentCache, a2aInvoker, agentCallGuard, taskEvents)
	reliableTaskWorker.Start()
	defer reliableTaskWorker.Stop()

	// 5.7 Agent 健康探测（网关主动探测 /health 端点）
	agentProber := service.NewAgentProber(agentRepo, agentCache, agentNotifier, instanceWatcher, instanceIP)
	agentProber.Start()
	defer agentProber.Stop()

	// 5.8 异步任务清理
	cleaner := service.NewTaskCleaner(taskRepo, taskWorker).
		WithZombieTimeout(30 * time.Minute)
	cleaner.Start()
	defer cleaner.Stop()

	// 5.9 预热
	logger.Info("starting warmup...")
	warmupStart := time.Now()
	if sqlDB, err := db.DB(); err == nil {
		sqlDB.SetMaxOpenConns(100)
		sqlDB.SetMaxIdleConns(50)
		sqlDB.SetConnMaxLifetime(10 * time.Minute)
		if err := sqlDB.Ping(); err != nil {
			logger.Warn("mysql warmup ping failed", zap.Error(err))
		}
	}
	if err := rdb.Ping(context.Background()).Err(); err != nil {
		logger.Warn("redis warmup ping failed", zap.Error(err))
	}
	logger.Info("warmup completed", zap.Duration("duration", time.Since(warmupStart)))

	// 6. Handler 初始化
	gatewayHandler = handler.NewGatewayHandler(agentCache, agentSkillRepo, agentPermRepo, a2aInvoker, agentCallGuard, taskRepo, reliableTaskRepo)

	agentHandler := handler.NewAgentHandler(agentSvc, agentPermRepo)
	agentApplyHandler := handler.NewAgentApplyHandler(agentApplyRepo, agentPermRepo, agentRepo)

	gatewayName := cfg.Telemetry.ServiceName
	if gatewayName == "" {
		gatewayName = "agent-gateway"
	}
	gatewayURL := fmt.Sprintf("http://localhost:%d", cfg.Server.Port)
	gatewayCardHandler := handler.NewGatewayCardHandler(agentRepo, agentSkillRepo, agentCache, gatewayName, gatewayURL)

	consumerHandler := handler.NewConsumerHandler(consumerRepo)
	authHandler := handler.NewAuthHandler(consumerRepo, &cfg.JWT)
	apiKeyHandler := handler.NewAPIKeyHandler(apiKeyRepo)
	mcpHandler := handler.NewMCPHandler(agentCache, agentSkillRepo, a2aInvoker, limiter, agentPermRepo)

	a2aProxyHandler := handler.NewA2AProxyHandler(agentCache, a2aClientPool, agentPermRepo)

	friendshipHandler := handler.NewFriendshipHandler(friendshipRepo, inboxHub)
	inboxHandler := handler.NewInboxHandler(onlineRegistry, inboxHub)
	taskV2Handler := handler.NewTaskV2Handler(agentDispatcher, taskV2Repo)
	monitorHandler := handler.NewMonitorHandler(agentRepo, taskV2Repo, monitorHub)

	var configHandler *handler.ConfigHandler
	if nacosWatcher != nil {
		configHandler = handler.NewConfigHandlerWithNacos(configRepo, rdb, nacosWatcher)
	} else {
		configHandler = handler.NewConfigHandler(configRepo, rdb)
	}

	// 7. Hertz 服务器
	h := server.Default(
		server.WithHostPorts(fmt.Sprintf(":%d", cfg.Server.Port)),
		server.WithMaxRequestBodySize(1<<20),
		server.WithALPN(true),
	)

	hlog.SetLogger(&logger.HertzLogger{})

	draining := middleware.NewDraining()
	h.Use(draining.Middleware())
	h.Use(middleware.RequestID())
	h.Use(middleware.CORS())
	h.Use(middleware.Tracing())
	h.Use(middleware.Metrics())
	h.Use(middleware.AccessLog())
	h.Use(middleware.CanaryMetrics())

	h.StaticFS("/", &app.FS{
		Root:        "./frontend",
		IndexNames:  []string{"index.html"},
		PathRewrite: app.NewPathSlashesStripper(0),
	})

	// 健康检查
	healthCheck := handler.NewHealthCheck(db, rdb)
	h.GET("/ping", healthCheck.Ping)
	h.GET("/health/deep", healthCheck.Check)
	h.GET("/health/ready", healthCheck.Ready)
	h.GET("/metrics", metrics.MetricsHandler())
	h.GET("/.well-known/agent.json", gatewayCardHandler.GetCard)

	// Skill 自升级分发 (不鉴权,详见 handler 文件注释)
	skillDistHandler := handler.NewSkillDistHandler("/app/skill-dist")
	h.GET("/skill/version", skillDistHandler.Version)
	h.GET("/skill/download", skillDistHandler.Download)

	// 中间件
	authMiddleware := middleware.Auth(&cfg.JWT, apiKeyRepo)
	adminToken := cfg.InternalToken
	if adminToken == "" {
		adminToken = os.Getenv("ADMIN_TOKEN")
	}
	if !middleware.RequireAdminToken(adminToken) {
		logger.Warn("admin api disabled: admin token not configured")
	}
	adminMiddleware := middleware.AdminAuth(adminToken)
	rateLimitMiddleware := middleware.RateLimit(limiter)

	// API Key 管理
	h.POST("/api-keys/generate", authMiddleware, apiKeyHandler.Generate)
	h.GET("/api-keys", authMiddleware, apiKeyHandler.Get)
	h.DELETE("/api-keys", authMiddleware, apiKeyHandler.Delete)

	// Consumer 自助注册（开放）。授权走 /agents/:agent_id/apply，由 agent owner 审批。
	h.POST("/register", consumerHandler.Register)

	// Auth
	h.POST("/auth/token", authHandler.Token)

	// Gateway 路由（A2A 调用）
	h.POST("/gateway/invoke/agent/:agent_id", authMiddleware, rateLimitMiddleware, gatewayHandler.InvokeAgent)
	h.POST("/gateway/invoke/agent/:agent_id/skill/:skill_id", authMiddleware, rateLimitMiddleware, gatewayHandler.InvokeAgent)
	h.GET("/gateway/task/:task_id", authMiddleware, gatewayHandler.GetTask)

	// MCP 路由
	h.GET("/mcp/sse", authMiddleware, mcpHandler.SSE)
	h.POST("/mcp/message", authMiddleware, mcpHandler.Message)

	// Admin 路由
	h.POST("/admin/config", authMiddleware, adminMiddleware, configHandler.UpdateConfig)
	h.GET("/admin/config/history", authMiddleware, adminMiddleware, configHandler.GetConfigHistory)
	h.POST("/admin/config/rollback", authMiddleware, adminMiddleware, configHandler.RollbackConfig)

	// Agent 注册/发现路由
	h.POST("/agents/register", authMiddleware, agentHandler.Register)
	h.DELETE("/agents/:agent_id", authMiddleware, agentHandler.Deregister)
	h.PUT("/agents/:agent_id/drain", authMiddleware, agentHandler.Drain)
	h.GET("/agents", agentHandler.List)
	h.GET("/agents/:agent_id", agentHandler.Get)
	h.GET("/agents/:agent_id/card", agentHandler.GetCard)
	h.GET("/agents/:agent_id/skills", agentHandler.GetSkills)

	// A2A 代理路由
	h.POST("/a2a/:agent_id", authMiddleware, rateLimitMiddleware, a2aProxyHandler.Proxy)
	h.GET("/a2a/:agent_id/.well-known/agent.json", agentHandler.GetCard)

	// Agent 权限申请路由
	h.POST("/agents/:agent_id/apply", authMiddleware, agentApplyHandler.Apply)
	h.GET("/agents/apply/inbox", authMiddleware, agentApplyHandler.Inbox)
	h.GET("/agents/apply/outbox", authMiddleware, agentApplyHandler.Outbox)
	h.POST("/agents/apply/:apply_id/approve", authMiddleware, agentApplyHandler.Approve)
	h.POST("/agents/apply/:apply_id/reject", authMiddleware, agentApplyHandler.Reject)
	h.DELETE("/agents/:agent_id/permissions/:consumer_app_id", authMiddleware, agentApplyHandler.Revoke)

	// GAS: Agent 身份中间件 (要求 X-Agent-ID 或 :agent_id)
	agentAuthMiddleware := middleware.AgentAuth(agentRepo)

	// GAS: 在线状态 + SSE inbox
	h.POST("/agents/online", authMiddleware, agentAuthMiddleware, inboxHandler.Online)
	h.POST("/agents/heartbeat", authMiddleware, agentAuthMiddleware, inboxHandler.Heartbeat)
	h.POST("/agents/offline", authMiddleware, agentAuthMiddleware, inboxHandler.Offline)
	h.GET("/a2a/inbox/stream", authMiddleware, agentAuthMiddleware, inboxHandler.Stream)

	// GAS: 好友关系
	h.POST("/friendships/request", authMiddleware, agentAuthMiddleware, friendshipHandler.Request)
	h.POST("/friendships/:id/accept", authMiddleware, agentAuthMiddleware, friendshipHandler.Accept)
	h.POST("/friendships/:id/reject", authMiddleware, agentAuthMiddleware, friendshipHandler.Reject)
	h.POST("/friendships/:id/revoke", authMiddleware, agentAuthMiddleware, friendshipHandler.Revoke)
	h.GET("/friendships", authMiddleware, agentAuthMiddleware, friendshipHandler.ListFriends)
	h.GET("/friendships/pending", authMiddleware, agentAuthMiddleware, friendshipHandler.ListPending)

	// GAS: Task / 消息
	h.POST("/v2/messages", authMiddleware, agentAuthMiddleware, taskV2Handler.SendMessage)
	h.GET("/v2/tasks", authMiddleware, agentAuthMiddleware, taskV2Handler.ListTasks)
	h.GET("/v2/tasks/:task_id", authMiddleware, agentAuthMiddleware, taskV2Handler.GetTask)
	h.POST("/v2/tasks/:task_id/close", authMiddleware, agentAuthMiddleware, taskV2Handler.CloseTask)
	h.POST("/v2/tasks/:task_id/read", authMiddleware, agentAuthMiddleware, taskV2Handler.MarkRead)

	// Web UI 监控流:账号级,不绑定特定 agent_id
	// 鉴权只走 Auth(JWT 或 API Key),不走 AgentAuth
	h.GET("/monitor/tasks", authMiddleware, monitorHandler.ListMyTasks)
	h.GET("/monitor/tasks/:task_id/messages", authMiddleware, monitorHandler.GetTaskMessages)
	h.GET("/monitor/stream", authMiddleware, monitorHandler.Stream)

	// 优雅关闭
	go func() {
		defer func() {
			if r := recover(); r != nil {
				logger.Error("graceful shutdown goroutine panic", zap.Any("recover", r))
			}
		}()

		quit := make(chan os.Signal, 1)
		signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
		<-quit
		logger.Info("shutting down server...")

		draining.Start()
		logger.Info("draining started, rejecting new requests")

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = h.Shutdown(ctx)

		stopCtx, stopCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer stopCancel()
		done := make(chan struct{})
		go func() {
			taskWorker.Stop()
			reliableTaskWorker.Stop()
			outboxDispatcher.Stop()
			close(done)
		}()
		select {
		case <-done:
			logger.Info("task worker stopped gracefully")
		case <-stopCtx.Done():
			logger.Warn("task worker stop timeout, forcing exit")
		}
	}()

	logger.Info("server started", zap.Int("port", cfg.Server.Port))
	if err := h.Run(); err != nil && err != http.ErrServerClosed {
		logger.Fatal("server run failed", zap.Error(err))
	}
}

// resolveInstanceIP 按优先级获取本实例 IP：
// 1. 环境变量 POD_IP（K8s/docker-compose 注入）
// 2. 出站连接的本地 IP（自动探测）
// 3. hostname（兜底）
func resolveInstanceIP() string {
	if ip := os.Getenv("POD_IP"); ip != "" {
		return ip
	}
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err == nil {
		defer conn.Close()
		return conn.LocalAddr().(*net.UDPAddr).IP.String()
	}
	hostname, _ := os.Hostname()
	return hostname
}

func initReliableTaskMQ(cfg config.AsyncMQConfig) (service.TaskEventPublisher, <-chan service.TaskEvent, func()) {
	mqType := strings.ToLower(strings.TrimSpace(cfg.Type))
	if mqType == "kafka" {
		if len(cfg.Brokers) == 0 {
			logger.Warn("async_mq.type is kafka but brokers is empty, falling back to memory queue")
		} else {
			queue := service.NewKafkaTaskQueue(service.KafkaTaskQueueConfig{
				Brokers: cfg.Brokers,
				Topic:   cfg.Topic,
				GroupID: cfg.GroupID,
				Buffer:  cfg.QueueBuffer,
			})
			queue.Start()
			logger.Info("reliable async task mq initialized",
				zap.String("type", "kafka"),
				zap.Strings("brokers", cfg.Brokers),
				zap.String("topic", cfg.Topic),
				zap.String("group_id", cfg.GroupID))
			return queue, queue.Subscribe(), func() {
				if err := queue.Close(); err != nil {
					logger.Warn("close kafka task queue failed", zap.Error(err))
				}
			}
		}
	}

	buffer := cfg.QueueBuffer
	if buffer <= 0 {
		buffer = 4096
	}
	queue := service.NewInMemoryTaskQueue(buffer)
	logger.Info("reliable async task mq initialized", zap.String("type", "memory"))
	return queue, queue.Subscribe(), func() {}
}
