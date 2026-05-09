package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
)

var bufPool = sync.Pool{
	New: func() any { return new(bytes.Buffer) },
}

// A2AInvoker 封装对 Agent 的 A2A JSON-RPC 调用，供 GatewayHandler 和 TaskWorker 共用
type A2AInvoker struct {
	pool *A2AClientPool
}

func NewA2AInvoker(pool *A2AClientPool) *A2AInvoker {
	return &A2AInvoker{pool: pool}
}

// Send 同步调用 agent（message/send），返回 JSON-RPC result 字段的原始 JSON。
// skillID 为空表示调用 agent 默认入口；非空时透传到下游用于精确路由。
func (inv *A2AInvoker) Send(ctx context.Context, agentURL string, input json.RawMessage, skillID string) (json.RawMessage, error) {
	body, err := buildA2ARequest("message/send", input, skillID)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, agentURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(req.Header))

	resp, err := inv.pool.Get(agentURL).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("agent returned %d: %s", resp.StatusCode, b)
	}

	buf := bufPool.Get().(*bytes.Buffer)
	buf.Reset()
	defer bufPool.Put(buf)

	if _, err := buf.ReadFrom(resp.Body); err != nil {
		return nil, err
	}

	var rpcResp struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(buf.Bytes(), &rpcResp); err != nil {
		return nil, err
	}
	if rpcResp.Error != nil {
		return nil, fmt.Errorf("a2a error %d: %s", rpcResp.Error.Code, rpcResp.Error.Message)
	}
	return rpcResp.Result, nil
}

// Stream 流式调用 agent（message/stream），将 SSE 响应写入 w。
func (inv *A2AInvoker) Stream(ctx context.Context, agentURL string, input json.RawMessage, skillID string, w io.Writer) error {
	body, err := buildA2ARequest("message/stream", input, skillID)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, agentURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(req.Header))

	resp, err := inv.pool.Get(agentURL).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("agent returned %d: %s", resp.StatusCode, b)
	}

	_, err = io.Copy(w, resp.Body)
	return err
}

// buildA2ARequest 构造 A2A JSON-RPC 请求体。
// skill_id 放在 params 层，未识别的下游可以安全忽略，已适配的下游可据此做多 skill 路由。
func buildA2ARequest(method string, input json.RawMessage, skillID string) ([]byte, error) {
	type a2aMessage struct {
		Role  string `json:"role"`
		Parts []struct {
			Type string          `json:"type"`
			Data json.RawMessage `json:"data"`
		} `json:"parts"`
	}
	type a2aParams struct {
		Message a2aMessage `json:"message"`
		SkillID string     `json:"skill_id,omitempty"`
	}
	type a2aRequest struct {
		JSONRPC string    `json:"jsonrpc"`
		Method  string    `json:"method"`
		Params  a2aParams `json:"params"`
		ID      int       `json:"id"`
	}

	req := a2aRequest{
		JSONRPC: "2.0",
		Method:  method,
		ID:      1,
		Params: a2aParams{
			SkillID: skillID,
			Message: a2aMessage{
				Role: "user",
				Parts: []struct {
					Type string          `json:"type"`
					Data json.RawMessage `json:"data"`
				}{
					{Type: "data", Data: input},
				},
			},
		},
	}
	return json.Marshal(req)
}
