// Package minigwlib 可复用的精简 Gateway 启动代码
//
// 被两处使用:
//   - test/e2e 下的 Go 测试(go test -tags e2e)
//   - test/e2e/cmd/minigw 下的独立 binary,供 Python e2e 起 subprocess
package minigwlib

import (
	"context"
	"fmt"
	"net"
	"os"
	"time"

	"agent-gateway/config"
	"agent-gateway/internal/handler"
	"agent-gateway/internal/middleware"
	"agent-gateway/internal/model"
	"agent-gateway/internal/repo"
	"agent-gateway/internal/service"

	"github.com/alicebob/miniredis/v2"
	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/network/standard"
	"github.com/redis/go-redis/v9"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// Instance 一个运行中的精简 Gateway
type Instance struct {
	Addr      string
	DB        *gorm.DB
	RDB       *redis.Client
	MiniRedis *miniredis.Miniredis
	H         *server.Hertz
	Cancel    context.CancelFunc

	AgentRepo      *repo.AgentRepo
	FriendshipRepo *repo.FriendshipRepo
	APIKeyRepo     *repo.APIKeyRepo
	ConsumerRepo   *repo.ConsumerRepo
}

// Start 启动一个精简 Gateway,自动选端口
func Start(sqlitePath string) (*Instance, error) {
	return StartWithOptions(Options{SQLitePath: sqlitePath, ListenAddr: "127.0.0.1:0"})
}

// Options 启动参数
type Options struct {
	SQLitePath string
	ListenAddr string // 格式 "host:port"。host 用 "0.0.0.0" 可让局域网可达
}

// StartWithOptions 可指定监听地址和 SQLite 路径
func StartWithOptions(opts Options) (*Instance, error) {
	// 默认:临时文件 + WAL 模式,比 in-memory 更能容忍并发写
	dsn := opts.SQLitePath
	if dsn == "" {
		f, err := os.CreateTemp("", "minigw-*.db")
		if err != nil {
			return nil, fmt.Errorf("tempfile: %w", err)
		}
		_ = f.Close()
		dsn = f.Name() + "?_journal_mode=WAL&_busy_timeout=5000&_foreign_keys=1"
	}
	listenAddr := opts.ListenAddr
	if listenAddr == "" {
		listenAddr = "127.0.0.1:0"
	}
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		return nil, fmt.Errorf("sqlite: %w", err)
	}
	if sqlDB, err := db.DB(); err == nil {
		sqlDB.SetMaxOpenConns(1) // SQLite 写锁限制,串行更安全
	}
	if err := db.AutoMigrate(
		&model.Consumer{}, &model.APIKey{}, &model.Agent{}, &model.AgentSkill{},
		&model.Friendship{}, &model.TaskV2{}, &model.TaskMember{}, &model.TaskMessage{},
	); err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}

	mr, err := miniredis.Run()
	if err != nil {
		return nil, fmt.Errorf("miniredis: %w", err)
	}
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})

	consumerRepo := repo.NewConsumerRepo(db)
	apiKeyRepo := repo.NewAPIKeyRepo(db)
	agentRepo := repo.NewAgentRepo(db)
	agentSkillRepo := repo.NewAgentSkillRepo(db)
	friendshipRepo := repo.NewFriendshipRepo(db)
	taskRepo := repo.NewTaskV2Repo(db)

	onlineReg := service.NewOnlineRegistry(rdb, 90*time.Second)
	hub := service.NewInboxHub()
	ctx, cancel := context.WithCancel(context.Background())
	hub.StartPingLoop(ctx, 30*time.Second)
	dispatcher := service.NewAgentDispatcher(agentRepo, friendshipRepo, taskRepo, onlineReg, hub, nil)

	agentCache := service.NewAgentCache(agentRepo)
	agentCache.SetNotifier(service.NewNoopAgentRegistryNotifier())
	if err := agentCache.Start(ctx); err != nil {
		cancel()
		return nil, fmt.Errorf("agent cache: %w", err)
	}
	agentSvc := service.NewAgentService(agentRepo, agentSkillRepo, agentCache, service.NewNoopAgentRegistryNotifier())

	consumerHandler := handler.NewConsumerHandler(consumerRepo)
	authHandler := handler.NewAuthHandler(consumerRepo, &config.JWTConfig{Secret: "test-secret"})
	apiKeyHandler := handler.NewAPIKeyHandler(apiKeyRepo)
	agentHandler := handler.NewAgentHandler(agentSvc, nil)

	friendshipHandler := handler.NewFriendshipHandler(friendshipRepo, hub)
	inboxHandler := handler.NewInboxHandler(onlineReg, hub)
	taskV2Handler := handler.NewTaskV2Handler(dispatcher, taskRepo)

	jwtCfg := &config.JWTConfig{Secret: "test-secret"}
	authMW := middleware.Auth(jwtCfg, apiKeyRepo)
	agentAuthMW := middleware.AgentAuth(agentRepo)

	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("listen: %w", err)
	}
	addr := ln.Addr().(*net.TCPAddr)

	h := server.New(
		server.WithListener(ln),
		server.WithTransport(standard.NewTransporter),
	)

	// 静态前端(根路径自动映射到 index.html)
	h.StaticFS("/", &app.FS{
		Root:        "./frontend",
		IndexNames:  []string{"index.html"},
		PathRewrite: app.NewPathSlashesStripper(0),
	})

	// Consumer 注册 + Auth
	h.POST("/register", consumerHandler.Register)
	h.POST("/auth/token", authHandler.Token)

	// API Key
	h.POST("/api-keys/generate", authMW, apiKeyHandler.Generate)
	h.GET("/api-keys", authMW, apiKeyHandler.Get)
	h.DELETE("/api-keys", authMW, apiKeyHandler.Delete)

	// Agent 注册/发现
	h.POST("/agents/register", authMW, agentHandler.Register)
	h.DELETE("/agents/:agent_id", authMW, agentHandler.Deregister)
	h.GET("/agents", agentHandler.List)
	h.GET("/agents/:agent_id", agentHandler.Get)
	h.GET("/agents/:agent_id/card", agentHandler.GetCard)
	h.GET("/agents/:agent_id/skills", agentHandler.GetSkills)

	h.POST("/friendships/request", authMW, agentAuthMW, friendshipHandler.Request)
	h.POST("/friendships/:id/accept", authMW, agentAuthMW, friendshipHandler.Accept)
	h.POST("/friendships/:id/reject", authMW, agentAuthMW, friendshipHandler.Reject)
	h.POST("/friendships/:id/revoke", authMW, agentAuthMW, friendshipHandler.Revoke)
	h.GET("/friendships", authMW, agentAuthMW, friendshipHandler.ListFriends)
	h.GET("/friendships/pending", authMW, agentAuthMW, friendshipHandler.ListPending)

	h.POST("/agents/online", authMW, agentAuthMW, inboxHandler.Online)
	h.POST("/agents/heartbeat", authMW, agentAuthMW, inboxHandler.Heartbeat)
	h.POST("/agents/offline", authMW, agentAuthMW, inboxHandler.Offline)
	h.GET("/a2a/inbox/stream", authMW, agentAuthMW, inboxHandler.Stream)

	h.POST("/v2/messages", authMW, agentAuthMW, taskV2Handler.SendMessage)
	h.GET("/v2/tasks", authMW, agentAuthMW, taskV2Handler.ListTasks)
	h.GET("/v2/tasks/:task_id", authMW, agentAuthMW, taskV2Handler.GetTask)
	h.POST("/v2/tasks/:task_id/close", authMW, agentAuthMW, taskV2Handler.CloseTask)

	go func() {
		_ = h.Run()
	}()

	return &Instance{
		Addr:           addr.String(),
		DB:             db,
		RDB:            rdb,
		MiniRedis:      mr,
		H:              h,
		Cancel:         cancel,
		AgentRepo:      agentRepo,
		FriendshipRepo: friendshipRepo,
		APIKeyRepo:     apiKeyRepo,
		ConsumerRepo:   consumerRepo,
	}, nil
}

// Stop 清理
func (i *Instance) Stop(ctx context.Context) {
	if i.Cancel != nil {
		i.Cancel()
	}
	_ = i.H.Shutdown(ctx)
	if i.RDB != nil {
		_ = i.RDB.Close()
	}
	if i.MiniRedis != nil {
		i.MiniRedis.Close()
	}
}
