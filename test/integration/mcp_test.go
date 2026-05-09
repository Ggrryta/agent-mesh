//go:build integration

package integration

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// mcpMessage 发送 JSON-RPC 消息到 /mcp/message
func mcpMessage(sessionID string, token string, method string, id any, params any) (*http.Response, error) {
	body := map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
	}
	if params != nil {
		body["params"] = params
	}
	b, _ := json.Marshal(body)
	req, err := http.NewRequest(http.MethodPost,
		fmt.Sprintf("%s/mcp/message?session_id=%s", baseURL, sessionID),
		bytes.NewBuffer(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	return http.DefaultClient.Do(req)
}

// TestMCPSSE 测试 SSE 连接建立并获取 session_id
func TestMCPSSE(t *testing.T) {
	_, token, err := createConsumerAndToken(nil)
	if err != nil {
		t.Fatalf("setup: %v", err)
	}

	// M1: 无 token → 401
	t.Run("M1_NoAuth", func(t *testing.T) {
		resp, err := http.Get(baseURL + "/mcp/sse")
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("expected 401, got %d", resp.StatusCode)
		}
	})

	// M2: 有效 token → 200 SSE，收到 endpoint event
	t.Run("M2_SSEConnectAndGetEndpoint", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodGet, baseURL+"/mcp/sse", nil)
		req.Header.Set("Authorization", "Bearer "+token)

		// 用带超时的 client，避免 SSE 长连接阻塞
		client := &http.Client{Timeout: 3 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			// 超时是预期的（SSE 长连接），只要拿到了响应头就行
			if resp == nil {
				t.Fatalf("request failed: %v", err)
			}
		}
		if resp != nil {
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("expected 200, got %d", resp.StatusCode)
			}
			ct := resp.Header.Get("Content-Type")
			if !strings.Contains(ct, "text/event-stream") {
				t.Fatalf("expected text/event-stream, got %s", ct)
			}
		}
	})
}

// TestMCPJSONRPC 测试 JSON-RPC 方法
func TestMCPJSONRPC(t *testing.T) {
	// 准备：注册一个 agent（含 skill）用于 tools/list 和 tools/call
	ts := time.Now().UnixNano()
	agentID := fmt.Sprintf("test-mcp-agent-%d", ts)
	skillID := fmt.Sprintf("test-mcp-skill-%d", ts)
	toolName := agentID + "/" + skillID // MCP tool name 格式

	_, token, err := createConsumerAndToken(nil)
	if err != nil {
		t.Fatalf("setup: %v", err)
	}

	r, _ := postJSONWithToken("/agents/register", map[string]any{
		"agent_id": agentID,
		"endpoint": "https://httpbin.org/post",
		"agent_card": map[string]any{
			"name":        "MCP test agent",
			"description": "MCP integration test",
			"version":     "1.0",
			"skills": []map[string]any{{
				"id":          skillID,
				"name":        "mcp test skill",
				"description": "MCP integration test skill",
			}},
		},
	}, token)
	if r != nil {
		r.Body.Close()
	}
	t.Cleanup(func() { deleteReqWithToken("/agents/"+agentID, token) })

	// 建立 SSE 连接，获取 session_id
	sessionID := ""
	t.Run("M3_EstablishSession", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodGet, baseURL+"/mcp/sse", nil)
		req.Header.Set("Authorization", "Bearer "+token)

		// 用 pipe 读取 SSE 流的第一个 event
		done := make(chan string, 1)
		go func() {
			client := &http.Client{}
			resp, err := client.Do(req)
			if err != nil || resp == nil {
				done <- ""
				return
			}
			defer resp.Body.Close()
			scanner := bufio.NewScanner(resp.Body)
			for scanner.Scan() {
				line := scanner.Text()
				// endpoint event data 格式: /mcp/message?session_id=xxx
				if strings.HasPrefix(line, "data: /mcp/message?session_id=") {
					sid := strings.TrimPrefix(line, "data: /mcp/message?session_id=")
					done <- sid
					return
				}
			}
			done <- ""
		}()

		select {
		case sid := <-done:
			if sid == "" {
				t.Fatal("failed to get session_id from SSE endpoint event")
			}
			sessionID = sid
			t.Logf("session_id: %s", sessionID)
		case <-time.After(3 * time.Second):
			t.Fatal("timeout waiting for SSE endpoint event")
		}
	})

	if sessionID == "" {
		t.Skip("no session_id, skipping message tests")
	}

	// M4: initialize → protocolVersion + capabilities
	t.Run("M4_Initialize", func(t *testing.T) {
		resp, err := mcpMessage(sessionID, token, "initialize", 1, map[string]any{
			"protocolVersion": "2024-11-05",
			"clientInfo":      map[string]any{"name": "test-client", "version": "1.0"},
		})
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
		}
		var result map[string]any
		json.NewDecoder(resp.Body).Decode(&result)
		res, _ := result["result"].(map[string]any)
		if res["protocolVersion"] != "2024-11-05" {
			t.Fatalf("expected protocolVersion 2024-11-05, got %v", res["protocolVersion"])
		}
		if res["capabilities"] == nil {
			t.Fatal("capabilities is nil")
		}
	})

	// M5: tools/list → 包含注册的 agent skill
	t.Run("M5_ToolsList", func(t *testing.T) {
		resp, err := mcpMessage(sessionID, token, "tools/list", 2, nil)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200, got %d", resp.StatusCode)
		}
		var result map[string]any
		json.NewDecoder(resp.Body).Decode(&result)
		res, _ := result["result"].(map[string]any)
		tools, _ := res["tools"].([]any)
		if len(tools) == 0 {
			t.Fatal("expected at least one tool")
		}
		found := false
		for _, tool := range tools {
			m, _ := tool.(map[string]any)
			if m["name"] == toolName {
				found = true
				if m["inputSchema"] == nil {
					t.Fatal("inputSchema is nil")
				}
			}
		}
		if !found {
			t.Fatalf("tool %s not found in tools/list", toolName)
		}
	})

	// M6: tools/call → isError=false，content 非空
	t.Run("M6_ToolsCall", func(t *testing.T) {
		resp, err := mcpMessage(sessionID, token, "tools/call", 3, map[string]any{
			"name":      toolName,
			"arguments": map[string]any{"key": "value"},
		})
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
		}
		var result map[string]any
		json.NewDecoder(resp.Body).Decode(&result)
		res, _ := result["result"].(map[string]any)
		if res == nil {
			t.Fatal("result is nil")
		}
		isError, _ := res["isError"].(bool)
		if isError {
			t.Fatalf("expected isError=false, got true. result: %v", res)
		}
		content, _ := res["content"].([]any)
		if len(content) == 0 {
			t.Fatal("content is empty")
		}
	})

	// M7: 未知方法 → error.code=-32601
	t.Run("M7_UnknownMethod", func(t *testing.T) {
		resp, err := mcpMessage(sessionID, token, "unknown/method", 4, nil)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200 (JSON-RPC error in body), got %d", resp.StatusCode)
		}
		var result map[string]any
		json.NewDecoder(resp.Body).Decode(&result)
		rpcErr, _ := result["error"].(map[string]any)
		if rpcErr == nil {
			t.Fatal("expected error in response")
		}
		code, _ := rpcErr["code"].(float64)
		if code != -32601 {
			t.Fatalf("expected error code -32601, got %v", code)
		}
	})

	// M8: 无效 session_id → 404
	t.Run("M8_InvalidSession", func(t *testing.T) {
		resp, err := mcpMessage("invalid-session-id", token, "initialize", 5, nil)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("expected 404, got %d", resp.StatusCode)
		}
	})
}
