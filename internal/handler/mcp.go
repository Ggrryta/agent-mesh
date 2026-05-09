package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"agent-gateway/internal/model"
	"agent-gateway/internal/repo"
	"agent-gateway/internal/service"
	"agent-gateway/pkg/ctxkey"
	"agent-gateway/pkg/logger"
	"agent-gateway/pkg/resp"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// MCPHandler 实现 MCP 协议（HTTP + SSE transport，spec 2024-11-05）
// tools 来源：所有 Active Agent 的 AgentSkill，tool name 格式：{agent_id}/{skill_id}
type MCPHandler struct {
	agentCache     *service.AgentCache
	agentSkillRepo mcpAgentSkillRepo
	agentPermRepo  *repo.AgentPermissionRepo
	a2aInvoker     a2aInvoker
	limiter        RateLimiter

	mu        sync.RWMutex
	sessions  map[string]*sessionInfo
	appCount  map[string]int
	maxPerApp int
}

// sessionInfo SSE 会话信息
type sessionInfo struct {
	ch    chan []byte
	appID string
}

// RateLimiter 限流器接口
type RateLimiter interface {
	Check(ctx context.Context, key string, limit int) error
}

type mcpAgentSkillRepo interface {
	ListByAgentID(ctx context.Context, agentID string) ([]*model.AgentSkill, error)
	GetByAgentIDAndSkillID(ctx context.Context, agentID, skillID string) (*model.AgentSkill, error)
}

func NewMCPHandler(agentCache *service.AgentCache, agentSkillRepo *repo.AgentSkillRepo, a2aInvoker *service.A2AInvoker, limiter RateLimiter, agentPermRepo *repo.AgentPermissionRepo) *MCPHandler {
	return &MCPHandler{
		agentCache:     agentCache,
		agentSkillRepo: agentSkillRepo,
		agentPermRepo:  agentPermRepo,
		a2aInvoker:     a2aInvoker,
		limiter:        limiter,
		sessions:       make(map[string]*sessionInfo),
		appCount:       make(map[string]int),
		maxPerApp:      10,
	}
}

// ---- JSON-RPC 结构 ----

type jsonRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type jsonRPCResponse struct {
	JSONRPC string        `json:"jsonrpc"`
	ID      any           `json:"id,omitempty"`
	Result  any           `json:"result,omitempty"`
	Error   *jsonRPCError `json:"error,omitempty"`
}

type jsonRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// ---- MCP Tool 格式 ----

type mcpTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

// ---- SSE: GET /mcp/sse ----

func (h *MCPHandler) SSE(ctx context.Context, c *app.RequestContext) {
	appID := c.GetString(ctxkey.AppID)

	h.mu.RLock()
	cnt := h.appCount[appID]
	h.mu.RUnlock()
	if cnt >= h.maxPerApp {
		c.JSON(consts.StatusServiceUnavailable, resp.Err(resp.CodeTooManyRequests, fmt.Sprintf("app %s has too many SSE connections (max %d)", appID, h.maxPerApp)))
		return
	}

	sessionID := uuid.New().String()
	ch := make(chan []byte, 32)

	h.mu.Lock()
	if h.appCount[appID] >= h.maxPerApp {
		h.mu.Unlock()
		c.JSON(consts.StatusServiceUnavailable, resp.Err(resp.CodeTooManyRequests, fmt.Sprintf("app %s has too many SSE connections (max %d)", appID, h.maxPerApp)))
		return
	}
	h.sessions[sessionID] = &sessionInfo{ch: ch, appID: appID}
	h.appCount[appID]++
	h.mu.Unlock()

	c.Response.Header.Set("Content-Type", "text/event-stream")
	c.Response.Header.Set("Cache-Control", "no-cache")
	c.Response.Header.Set("X-Accel-Buffering", "no")
	c.Response.Header.SetStatusCode(consts.StatusOK)

	pr, pw := io.Pipe()
	c.SetBodyStream(pr, -1)

	go func() {
		defer func() {
			if r := recover(); r != nil {
				logger.Error("mcp sse goroutine panic",
					zap.String("session_id", sessionID),
					zap.Any("recover", r))
			}
			h.mu.Lock()
			delete(h.sessions, sessionID)
			h.appCount[appID]--
			if h.appCount[appID] <= 0 {
				delete(h.appCount, appID)
			}
			h.mu.Unlock()
			close(ch)
			pw.Close()
		}()

		endpointEvent := fmt.Sprintf("event: endpoint\ndata: /mcp/message?session_id=%s\n\n", sessionID)
		if _, err := pw.Write([]byte(endpointEvent)); err != nil {
			return
		}

		var buf bytes.Buffer
		buf.Grow(256)

		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case msg, ok := <-ch:
				if !ok {
					return
				}
				buf.Reset()
				buf.WriteString("data: ")
				buf.Write(msg)
				buf.WriteString("\n\n")
				if _, err := pw.Write(buf.Bytes()); err != nil {
					return
				}
			case <-ticker.C:
				if _, err := pw.Write([]byte(": ping\n\n")); err != nil {
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}()
}

// ---- Message: POST /mcp/message ----

func (h *MCPHandler) Message(ctx context.Context, c *app.RequestContext) {
	sessionID := string(c.Query("session_id"))
	h.mu.RLock()
	_, ok := h.sessions[sessionID]
	h.mu.RUnlock()
	if !ok {
		c.JSON(consts.StatusNotFound, resp.Err(resp.CodeNotFound, "session not found: "+sessionID))
		return
	}

	var req jsonRPCRequest
	if err := c.BindJSON(&req); err != nil {
		c.JSON(consts.StatusBadRequest, resp.Err(resp.CodeBadRequest, "invalid JSON-RPC request"))
		return
	}

	var result any
	var rpcErr *jsonRPCError

	appID := c.GetString(ctxkey.AppID)

	switch req.Method {
	case "initialize":
		result = h.handleInitialize()
	case "tools/list":
		result, rpcErr = h.handleToolsList(ctx, appID)
	case "tools/call":
		result, rpcErr = h.handleToolsCall(ctx, appID, req.Params)
	default:
		rpcErr = &jsonRPCError{Code: -32601, Message: "method not found: " + req.Method}
	}

	c.JSON(consts.StatusOK, jsonRPCResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result:  result,
		Error:   rpcErr,
	})
}

// ---- JSON-RPC 方法实现 ----

func (h *MCPHandler) handleInitialize() any {
	return map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities": map[string]any{
			"tools": map[string]any{},
		},
		"serverInfo": map[string]any{
			"name":    "agent-gateway",
			"version": "1.0.0",
		},
	}
}

func (h *MCPHandler) handleToolsList(ctx context.Context, appID string) (any, *jsonRPCError) {
	agents := h.agentCache.ListActive()
	tools := make([]mcpTool, 0, len(agents)*4)

	for _, agent := range agents {
		if agent.Visibility == model.VisibilityPrivate && appID != agent.OwnerAppID {
			has, err := h.agentPermRepo.HasPermission(ctx, agent.AgentID, appID)
			if err != nil {
				logger.Warn("mcp: check permission failed",
					zap.String("agent_id", agent.AgentID), zap.Error(err))
				continue
			}
			if !has {
				continue
			}
		}
		skills, err := h.agentSkillRepo.ListByAgentID(ctx, agent.AgentID)
		if err != nil {
			logger.Warn("mcp: list agent skills failed",
				zap.String("agent_id", agent.AgentID),
				zap.Error(err))
			continue
		}
		for _, s := range skills {
			tools = append(tools, mcpTool{
				Name:        agent.AgentID + "/" + s.SkillID,
				Description: s.Description,
				InputSchema: map[string]any{"type": "object"},
			})
		}
	}

	return map[string]any{"tools": tools}, nil
}

func (h *MCPHandler) handleToolsCall(ctx context.Context, appID string, params json.RawMessage) (any, *jsonRPCError) {
	var p struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &jsonRPCError{Code: -32602, Message: "invalid params: " + err.Error()}
	}

	// tool name 格式：{agent_id}/{skill_id}
	parts := strings.SplitN(p.Name, "/", 2)
	if len(parts) != 2 {
		return nil, &jsonRPCError{Code: -32602, Message: "invalid tool name format, expected {agent_id}/{skill_id}: " + p.Name}
	}
	agentID := parts[0]
	skillID := parts[1]

	agent, ok := h.agentCache.Get(agentID)
	if !ok {
		return nil, &jsonRPCError{Code: -32602, Message: "agent not found or not active: " + agentID}
	}
	if h.agentSkillRepo == nil {
		return nil, &jsonRPCError{Code: -32603, Message: "agent skill repo is not configured"}
	}
	if _, err := h.agentSkillRepo.GetByAgentIDAndSkillID(ctx, agentID, skillID); err != nil {
		if errors.Is(err, repo.ErrAgentSkillNotFound) {
			return nil, &jsonRPCError{Code: -32602, Message: "skill not found for agent: " + p.Name}
		}
		return nil, &jsonRPCError{Code: -32603, Message: "query agent skill failed: " + err.Error()}
	}

	if agent.Visibility == model.VisibilityPrivate && appID != agent.OwnerAppID {
		has, err := h.agentPermRepo.HasPermission(ctx, agent.AgentID, appID)
		if err != nil {
			return nil, &jsonRPCError{Code: -32603, Message: "permission check failed: " + err.Error()}
		}
		if !has {
			return nil, &jsonRPCError{Code: -32603, Message: "no permission to call agent: " + agentID}
		}
	}

	input := p.Arguments
	if len(input) == 0 {
		input = json.RawMessage("{}")
	}

	output, err := h.a2aInvoker.Send(ctx, agent.URL, input, skillID)
	if err != nil {
		return nil, &jsonRPCError{Code: -32603, Message: "invoke agent failed: " + err.Error()}
	}

	return map[string]any{
		"content": []map[string]any{
			{"type": "text", "text": string(output)},
		},
		"isError": false,
	}, nil
}
