package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"
)

const baseURL = "http://localhost:8080"

func postJSON(path string, payload any) (*http.Response, error) {
	body, _ := json.Marshal(payload)
	req, err := http.NewRequest(http.MethodPost, baseURL+path, bytes.NewBuffer(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	return http.DefaultClient.Do(req)
}

func postJSONWithToken(path string, payload any, token string) (*http.Response, error) {
	body, _ := json.Marshal(payload)
	req, err := http.NewRequest(http.MethodPost, baseURL+path, bytes.NewBuffer(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	return http.DefaultClient.Do(req)
}

func deleteReq(path string) {
	req, _ := http.NewRequest(http.MethodDelete, baseURL+path, nil)
	resp, _ := http.DefaultClient.Do(req)
	if resp != nil {
		resp.Body.Close()
	}
}

// TestAgentE2EWorkflow 端到端：注册 agent → 创建 consumer → 获取 token → 调用 agent
func TestAgentE2EWorkflow(t *testing.T) {
	ts := time.Now().UnixNano()
	agentID := fmt.Sprintf("test-e2e-agent-%d", ts)
	skillID := fmt.Sprintf("test-e2e-skill-%d", ts)

	// E1: 注册 consumer
	appID := fmt.Sprintf("test-e2e-app-%d", ts)
	secret := "e2e-test-secret-12345"
	t.Run("E1_RegisterConsumer", func(t *testing.T) {
		resp, err := postJSON("/register", map[string]string{
			"app_id":      appID,
			"secret":      secret,
			"description": "e2e test consumer",
		})
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
		}
	})

	// E2: 获取 token
	var token string
	t.Run("E2_GetToken", func(t *testing.T) {
		resp, err := postJSON("/auth/token", map[string]string{
			"app_id": appID,
			"secret": secret,
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
		token, _ = result["data"].(map[string]any)["token"].(string)
		if token == "" {
			t.Fatal("token is empty")
		}
	})

	if token == "" {
		t.Skip("no token, skipping remaining tests")
	}

	// E3: 注册 agent
	t.Run("E3_RegisterAgent", func(t *testing.T) {
		resp, err := postJSONWithToken("/agents/register", map[string]any{
			"agent_id": agentID,
			"endpoint": "https://httpbin.org/post",
			"agent_card": map[string]any{
				"name":        "E2E test agent",
				"description": "end-to-end test",
				"version":     "1.0",
				"skills": []map[string]any{{
					"id":          skillID,
					"name":        "e2e test skill",
					"description": "end-to-end test skill",
				}},
			},
		}, token)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
		}
	})
	t.Cleanup(func() {
		req, _ := http.NewRequest(http.MethodDelete, baseURL+"/agents/"+agentID, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		resp, _ := http.DefaultClient.Do(req)
		if resp != nil {
			resp.Body.Close()
		}
	})

	// E4: 调用 agent → 通过鉴权（非 401/403）
	t.Run("E4_InvokeAgent", func(t *testing.T) {
		resp, err := postJSONWithToken("/gateway/invoke/agent/"+agentID, map[string]any{
			"input": map[string]any{"e2e": true},
		}, token)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("auth failed: %d: %s", resp.StatusCode, body)
		}
	})

	// E5: 调用 agent skill → 通过鉴权
	t.Run("E5_InvokeAgentSkill", func(t *testing.T) {
		resp, err := postJSONWithToken("/gateway/invoke/agent/"+agentID+"/skill/"+skillID, map[string]any{
			"input": map[string]any{"e2e": true},
		}, token)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("auth failed: %d: %s", resp.StatusCode, body)
		}
	})
}
