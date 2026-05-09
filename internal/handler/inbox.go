package handler

import (
	"context"
	"io"

	"agent-gateway/internal/service"
	"agent-gateway/pkg/ctxkey"
	"agent-gateway/pkg/logger"
	"agent-gateway/pkg/resp"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
	"go.uber.org/zap"
)

// InboxHandler 负责 GAS 接入 Gateway 的 SSE 事件流 + 心跳
// 流程:
//  1. GAS 启动 Agent Core 后,POST /agents/online  标记上线
//  2. GAS 建立 GET /a2a/inbox/stream 长连接,订阅事件
//  3. GAS 每 30s POST /agents/heartbeat 续约
//  4. GAS 退出时 POST /agents/offline 主动下线
type InboxHandler struct {
	online *service.OnlineRegistry
	hub    *service.InboxHub
}

func NewInboxHandler(online *service.OnlineRegistry, hub *service.InboxHub) *InboxHandler {
	return &InboxHandler{online: online, hub: hub}
}

// Online POST /agents/online
// body: {gas_instance_id, ip?}
// 需要 AgentAuth 已注入 ctxkey.AgentID
func (h *InboxHandler) Online(ctx context.Context, c *app.RequestContext) {
	self := c.GetString(ctxkey.AgentID)
	var req struct {
		GASInstanceID string `json:"gas_instance_id"`
		IP            string `json:"ip"`
	}
	if err := c.BindJSON(&req); err != nil {
		c.JSON(consts.StatusBadRequest, resp.Err(resp.CodeBadRequest, "invalid body"))
		return
	}
	if req.GASInstanceID == "" {
		c.JSON(consts.StatusBadRequest, resp.Err(resp.CodeBadRequest, "gas_instance_id required"))
		return
	}
	err := h.online.Online(ctx, service.AgentOnlineInfo{
		AgentID:       self,
		GASInstanceID: req.GASInstanceID,
		IP:            req.IP,
	})
	if err != nil {
		if err == service.ErrAgentConflict {
			c.JSON(consts.StatusConflict,
				resp.Err(resp.CodeAgentConflict, "agent already online elsewhere"))
			return
		}
		c.JSON(consts.StatusInternalServerError, resp.Err(resp.CodeInternalServerError, err.Error()))
		return
	}
	c.JSON(consts.StatusOK, resp.OK(nil))
}

// Heartbeat POST /agents/heartbeat
// body: {gas_instance_id}
func (h *InboxHandler) Heartbeat(ctx context.Context, c *app.RequestContext) {
	self := c.GetString(ctxkey.AgentID)
	var req struct {
		GASInstanceID string `json:"gas_instance_id"`
	}
	if err := c.BindJSON(&req); err != nil {
		c.JSON(consts.StatusBadRequest, resp.Err(resp.CodeBadRequest, "invalid body"))
		return
	}
	if err := h.online.Heartbeat(ctx, self, req.GASInstanceID); err != nil {
		if err == service.ErrAgentConflict {
			c.JSON(consts.StatusConflict,
				resp.Err(resp.CodeAgentConflict, "agent not online under this instance"))
			return
		}
		c.JSON(consts.StatusInternalServerError, resp.Err(resp.CodeInternalServerError, err.Error()))
		return
	}
	c.JSON(consts.StatusOK, resp.OK(nil))
}

// Offline POST /agents/offline
// body: {gas_instance_id}
func (h *InboxHandler) Offline(ctx context.Context, c *app.RequestContext) {
	self := c.GetString(ctxkey.AgentID)
	var req struct {
		GASInstanceID string `json:"gas_instance_id"`
	}
	if err := c.BindJSON(&req); err != nil {
		c.JSON(consts.StatusBadRequest, resp.Err(resp.CodeBadRequest, "invalid body"))
		return
	}
	if err := h.online.Offline(ctx, self, req.GASInstanceID); err != nil && err != service.ErrAgentConflict {
		c.JSON(consts.StatusInternalServerError, resp.Err(resp.CodeInternalServerError, err.Error()))
		return
	}
	c.JSON(consts.StatusOK, resp.OK(nil))
}

// Stream GET /a2a/inbox/stream
// SSE 长连接,推送 InboxEvent
func (h *InboxHandler) Stream(ctx context.Context, c *app.RequestContext) {
	self := c.GetString(ctxkey.AgentID)
	session := h.hub.Subscribe(self)
	// 注意:不能在此 defer Unsubscribe,因为 Hertz handler 会立即返回
	// (SetBodyStream 交付 reader 后,handler 退出,goroutine 才真正处理流)
	// Unsubscribe 必须在 goroutine 退出时执行。

	c.Response.Header.Set("Content-Type", "text/event-stream")
	c.Response.Header.Set("Cache-Control", "no-cache")
	c.Response.Header.Set("Connection", "keep-alive")
	c.Response.Header.Set("X-Accel-Buffering", "no")
	c.Response.SetStatusCode(consts.StatusOK)

	pr, pw := io.Pipe()
	c.SetBodyStream(pr, -1)

	go func() {
		defer func() {
			if r := recover(); r != nil {
				logger.Error("inbox stream panic", zap.Any("recover", r))
			}
			h.hub.Unsubscribe(self, session)
			pw.Close()
		}()

		logger.Info("inbox stream opened", zap.String("agent_id", self))

		// 发送一个 hello 事件,立即 flush 让客户端知道连接就绪
		if _, err := pw.Write(service.EncodeSSE(&service.InboxEvent{
			Kind: service.InboxEventPing,
			Data: map[string]any{"hello": self},
		})); err != nil {
			return
		}

		for {
			select {
			case <-ctx.Done():
				logger.Info("inbox stream: client disconnected",
					zap.String("agent_id", self))
				return
			case <-session.Done:
				logger.Info("inbox stream: session evicted",
					zap.String("agent_id", self))
				return
			case evt, ok := <-session.Events:
				if !ok {
					return
				}
				if _, err := pw.Write(service.EncodeSSE(evt)); err != nil {
					logger.Info("inbox stream: write failed",
						zap.String("agent_id", self), zap.Error(err))
					return
				}
			}
		}
	}()
}
