package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"agent-gateway/internal/repo"
	"agent-gateway/internal/service"
	"agent-gateway/pkg/ctxkey"
	"agent-gateway/pkg/logger"
	"agent-gateway/pkg/metrics"
	"agent-gateway/pkg/resp"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.uber.org/zap"
)

const (
	a2aMethodStream      = "message/stream"
	a2aMethodResubscribe = "tasks/resubscribe"
	contentTypeSSE       = "text/event-stream"
	contentTypeJSON      = "application/json"
)

// A2AProxyHandler 接收 A2A 调用，查找上游 Agent，透传请求
type A2AProxyHandler struct {
	cache      *service.AgentCache
	clientPool *service.A2AClientPool
	permRepo   *repo.AgentPermissionRepo
}

func NewA2AProxyHandler(cache *service.AgentCache, clientPool *service.A2AClientPool, permRepo *repo.AgentPermissionRepo) *A2AProxyHandler {
	return &A2AProxyHandler{cache: cache, clientPool: clientPool, permRepo: permRepo}
}

// Proxy POST /a2a/:agent_id
func (h *A2AProxyHandler) Proxy(ctx context.Context, c *app.RequestContext) {
	agentID := c.Param("agent_id")
	callerAppID := c.GetString(ctxkey.AppID)

	agent, ok := h.cache.Get(agentID)
	if !ok {
		c.JSON(consts.StatusNotFound, resp.Err(resp.CodeNotFound, fmt.Sprintf("agent %q not found or inactive", agentID)))
		return
	}

	// 权限检查：owner 本身始终有权限；其他 consumer 需要授权记录
	if callerAppID != agent.OwnerAppID {
		allowed, err := h.permRepo.HasPermission(ctx, agentID, callerAppID)
		if err != nil {
			logger.Error("a2a proxy: check permission failed", zap.String("agent_id", agentID), zap.Error(err))
			c.JSON(consts.StatusInternalServerError, resp.Err(resp.CodeInternalServerError, "permission check failed"))
			return
		}
		if !allowed {
			c.JSON(consts.StatusForbidden, resp.Err(resp.CodeForbidden, "no permission to call this agent"))
			return
		}
	}

	body := c.Request.Body()

	// 判断是否为流式方法
	streaming := isStreamingMethod(body)

	upstreamURL := strings.TrimSuffix(agent.URL, "/")
	httpClient := h.clientPool.Get(upstreamURL)

	start := time.Now()
	if streaming {
		h.proxyStream(ctx, c, httpClient, upstreamURL, body, agentID)
	} else {
		h.proxySync(ctx, c, httpClient, upstreamURL, body, agentID)
	}
	elapsed := time.Since(start).Seconds()
	status := strconv.Itoa(c.Response.StatusCode())
	metrics.A2AProxyTotal.WithLabelValues(agentID, status).Inc()
	metrics.A2AProxyDuration.WithLabelValues(agentID).Observe(elapsed)
}

// proxySync 同步调用：转发请求，等待完整响应后回写
func (h *A2AProxyHandler) proxySync(ctx context.Context, c *app.RequestContext, httpClient *http.Client, upstreamURL string, body []byte, agentID string) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, upstreamURL+"/", bytes.NewReader(body))
	if err != nil {
		logger.Error("a2a proxy: create request failed", zap.String("agent_id", agentID), zap.Error(err))
		c.JSON(consts.StatusBadGateway, resp.Err(resp.CodeBadGateway, "failed to create upstream request"))
		return
	}
	req.Header.Set("Content-Type", contentTypeJSON)
	req.Header.Set("Accept", contentTypeJSON)
	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(req.Header))

	upstreamResp, err := httpClient.Do(req)
	if err != nil {
		logger.Error("a2a proxy: upstream call failed", zap.String("agent_id", agentID), zap.Error(err))
		c.JSON(consts.StatusBadGateway, resp.Err(resp.CodeBadGateway, "upstream agent unreachable"))
		return
	}
	defer upstreamResp.Body.Close()

	respBody, err := io.ReadAll(upstreamResp.Body)
	if err != nil {
		logger.Error("a2a proxy: read upstream response failed", zap.String("agent_id", agentID), zap.Error(err))
		c.JSON(consts.StatusBadGateway, resp.Err(resp.CodeBadGateway, "failed to read upstream response"))
		return
	}

	c.Response.Header.Set("Content-Type", contentTypeJSON)
	c.Response.SetStatusCode(upstreamResp.StatusCode)
	c.Response.SetBody(respBody)
}

// proxyStream SSE 流式调用：建立上游连接后逐块透传 SSE 事件
func (h *A2AProxyHandler) proxyStream(ctx context.Context, c *app.RequestContext, httpClient *http.Client, upstreamURL string, body []byte, agentID string) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, upstreamURL+"/", bytes.NewReader(body))
	if err != nil {
		logger.Error("a2a proxy stream: create request failed", zap.String("agent_id", agentID), zap.Error(err))
		c.JSON(consts.StatusBadGateway, resp.Err(resp.CodeBadGateway, "failed to create upstream request"))
		return
	}
	req.Header.Set("Content-Type", contentTypeJSON)
	req.Header.Set("Accept", contentTypeSSE)
	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(req.Header))

	upstreamResp, err := httpClient.Do(req)
	if err != nil {
		logger.Error("a2a proxy stream: upstream call failed", zap.String("agent_id", agentID), zap.Error(err))
		c.JSON(consts.StatusBadGateway, resp.Err(resp.CodeBadGateway, "upstream agent unreachable"))
		return
	}
	defer upstreamResp.Body.Close()

	if upstreamResp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(upstreamResp.Body)
		c.Response.SetStatusCode(upstreamResp.StatusCode)
		c.Response.Header.Set("Content-Type", contentTypeJSON)
		if len(errBody) > 0 {
			c.Response.SetBody(errBody)
		} else {
			c.Response.SetBodyString(fmt.Sprintf(`{"error":"upstream returned %d"}`, upstreamResp.StatusCode))
		}
		return
	}

	// 设置 SSE 响应头
	c.Response.Header.Set("Content-Type", contentTypeSSE)
	c.Response.Header.Set("Cache-Control", "no-cache")
	c.Response.Header.Set("Connection", "keep-alive")
	c.Response.Header.Set("X-Accel-Buffering", "no")
	c.Response.SetStatusCode(http.StatusOK)

	// 零拷贝透传 SSE 流
	buf := make([]byte, 4096)
	w := c.Response.BodyWriter()
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		n, readErr := upstreamResp.Body.Read(buf)
		if n > 0 {
			if _, writeErr := w.Write(buf[:n]); writeErr != nil {
				logger.Debug("a2a proxy stream: client disconnected", zap.String("agent_id", agentID))
				return
			}
			// 刷新缓冲区，确保 SSE 事件立即送达
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
		}
		if readErr == io.EOF {
			return
		}
		if readErr != nil {
			logger.Error("a2a proxy stream: read upstream failed", zap.String("agent_id", agentID), zap.Error(readErr))
			return
		}
	}
}

// isStreamingMethod 解析 JSON-RPC body，判断是否为流式方法
func isStreamingMethod(body []byte) bool {
	var req struct {
		Method string `json:"method"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return false
	}
	return req.Method == a2aMethodStream || req.Method == a2aMethodResubscribe
}
