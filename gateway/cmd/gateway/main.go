// API Gateway: 纯路由层。按路径前缀转发到 Identity / Messaging / Push 后端服务。
// 职责：路由、JWT 验证、全局限流、CORS、日志。不做任何业务逻辑。
package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/Ggrryta/agent-mesh/gateway/internal/middleware"
	"github.com/Ggrryta/agent-mesh/gateway/internal/observability/health"
	"github.com/Ggrryta/agent-mesh/gateway/internal/observability/logger"
	"github.com/Ggrryta/agent-mesh/gateway/internal/ratelimit"
	"github.com/Ggrryta/agent-mesh/gateway/pkg/auth"

	"github.com/gorilla/websocket"
	goredis "github.com/redis/go-redis/v9"

	"go.uber.org/zap"
)

func main() {
	logLevel := envOr("LOG_LEVEL", "info")
	podName := envOr("POD_NAME", "api-gateway")
	log, err := logger.New(logLevel, "json", podName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "logger: %v\n", err)
		os.Exit(1)
	}
	defer log.Sync()

	probe := health.New()

	// JWT Signer（用于 Gateway 层统一认证）
	jwtSecret := envOr("JWT_SECRET", "")
	if jwtSecret == "" {
		log.Fatal("JWT_SECRET env is required")
	}
	signer, err := auth.NewSigner(jwtSecret, 24*time.Hour, 1*time.Hour)
	if err != nil {
		log.Fatal("auth signer", zap.Error(err))
	}

	// 后端服务地址
	identityURL := mustParseURL(envOr("IDENTITY_URL", "http://127.0.0.1:8081"))
	messagingURL := mustParseURL(envOr("MESSAGING_URL", "http://127.0.0.1:8082"))
	pushURL := mustParseURL(envOr("PUSH_URL", "http://127.0.0.1:8083"))

	// 反向代理
	identityProxy := httputil.NewSingleHostReverseProxy(identityURL)
	messagingProxy := httputil.NewSingleHostReverseProxy(messagingURL)

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", probe.LivenessHandler())
	mux.HandleFunc("/readyz", probe.ReadinessHandler())

	// 路由规则（Go ServeMux 最长前缀匹配）：
	// 具体路径优先匹配，兜底规则 /v1/admin/ 和 /v1/mesh/ 走 Identity

	// Messaging 路由（具体路径，优先于兜底）+ per-agent 限流
	rlCfg := ratelimit.Config{
		RequestsPerSecond: envInt("RATELIMIT_RPS", 50),
		BurstSize:         envInt("RATELIMIT_BURST", 100),
		GlobalRPS:         envInt("RATELIMIT_GLOBAL_RPS", 2000),
		GlobalBurst:       envInt("RATELIMIT_GLOBAL_BURST", 4000),
	}
	var limiter ratelimit.RateLimiter
	if redisAddr := envOr("REDIS_ADDR", ""); redisAddr != "" {
		rdb := goredis.NewClient(&goredis.Options{Addr: redisAddr})
		limiter = ratelimit.NewRedisLimiter(rdb, rlCfg, log)
		_ = ratelimit.NewConfigWatcher(rdb, limiter, log)
		log.Info("ratelimit: using Redis", zap.String("addr", redisAddr))
	} else {
		limiter = ratelimit.NewLocalLimiter(rlCfg)
		log.Info("ratelimit: using local (in-memory)")
	}
	mux.HandleFunc("/v1/admin/friends/", proxy(messagingProxy))
	mux.HandleFunc("/v1/admin/groups/", proxy(messagingProxy))
	mux.HandleFunc("/v1/admin/tasks", proxy(messagingProxy))
	mux.HandleFunc("/v1/admin/tasks/", proxy(messagingProxy))
	mux.Handle("/v1/mesh/tasks/", limiter.Middleware(http.HandlerFunc(proxy(messagingProxy))))
	mux.Handle("/v1/mesh/tasks", limiter.Middleware(http.HandlerFunc(proxy(messagingProxy))))
	mux.Handle("/v1/mesh/inbox", limiter.Middleware(http.HandlerFunc(proxy(messagingProxy))))

	// Push Gateway 路由（WebSocket 需要特殊处理）
	// httputil.ReverseProxy 不支持 WebSocket，直接透传到 Push Gateway
	mux.HandleFunc("/v1/admin/ws/", func(w http.ResponseWriter, r *http.Request) {
		// WebSocket 请求直接代理到 Push Gateway（不走 ReverseProxy）
		target := pushURL.Host
		wsURL := "ws://" + target + r.URL.Path + "?" + r.URL.RawQuery

		// 连接后端 WebSocket
		backendConn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
		if err != nil {
			http.Error(w, "push gateway unavailable", http.StatusBadGateway)
			return
		}
		defer backendConn.Close()

		// 升级前端连接
		upgrader := websocket.Upgrader{
			ReadBufferSize:  1024,
			WriteBufferSize: 4096,
			CheckOrigin:     func(r *http.Request) bool { return true },
		}
		clientConn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer clientConn.Close()

		// 双向代理
		done := make(chan struct{})
		// backend → client
		go func() {
			defer close(done)
			for {
				msgType, msg, err := backendConn.ReadMessage()
				if err != nil { return }
				if err := clientConn.WriteMessage(msgType, msg); err != nil { return }
			}
		}()
		// client → backend
		for {
			msgType, msg, err := clientConn.ReadMessage()
			if err != nil { break }
			if err := backendConn.WriteMessage(msgType, msg); err != nil { break }
		}
		<-done
	})

	// Identity 兜底：所有其他 /v1/admin/* 和 /v1/mesh/* 走 Identity
	mux.HandleFunc("/v1/admin/", proxy(identityProxy))
	mux.HandleFunc("/v1/mesh/", proxy(identityProxy))

	// 中间件链：CORS + RequestID + Recover + GatewayAuth + Metrics + AccessLog
	handler := middleware.CORS(nil)(
		middleware.WithRequestID(
			middleware.Recover(log)(
				middleware.GatewayAuth(signer)(
					middleware.Metrics(
						middleware.AccessLog(log)(mux),
					),
				),
			),
		),
	)

	addr := envOr("GATEWAY_ADDR", ":8080")
	srv := &http.Server{
		Addr:         addr,
		Handler:      handler,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 60 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	go func() {
		log.Info("API Gateway started", zap.String("addr", addr),
			zap.String("identity", identityURL.String()),
			zap.String("messaging", messagingURL.String()),
			zap.String("push", pushURL.String()))
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal("http", zap.Error(err))
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Info("shutting down")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	srv.Shutdown(ctx)
}

func proxy(p *httputil.ReverseProxy) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p.ServeHTTP(w, r)
	}
}

func mustParseURL(raw string) *url.URL {
	u, err := url.Parse(raw)
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid URL %q: %v\n", raw, err)
		os.Exit(1)
	}
	return u
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}
