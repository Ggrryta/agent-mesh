package integration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"
)

// postJSONWithToken 带 Authorization header 的 POST 请求
func postJSONWithToken(path string, payload any, token string) (*http.Response, error) {
	body, _ := json.Marshal(payload)
	req, err := http.NewRequest(http.MethodPost, baseURL+path, bytes.NewBuffer(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return http.DefaultClient.Do(req)
}

// createConsumerAndToken 注册 Consumer 并获取 JWT token，返回 (appID, token, error)
func createConsumerAndToken(_ []string) (string, string, error) {
	appID := fmt.Sprintf("test.auth.%d", time.Now().UnixNano())
	secret := "test-secret-12345"

	// 注册 consumer
	createResp, err := postJSON("/register", map[string]string{
		"app_id":      appID,
		"secret":      secret,
		"description": "auth test consumer",
	})
	if err != nil {
		return "", "", fmt.Errorf("register consumer: %w", err)
	}
	defer createResp.Body.Close()
	if createResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(createResp.Body)
		return "", "", fmt.Errorf("register consumer failed %d: %s", createResp.StatusCode, body)
	}

	// 获取 token
	tokenResp, err := postJSON("/auth/token", map[string]string{
		"app_id": appID,
		"secret": secret,
	})
	if err != nil {
		return "", "", fmt.Errorf("get token: %w", err)
	}
	defer tokenResp.Body.Close()
	var tokenResult map[string]any
	json.NewDecoder(tokenResp.Body).Decode(&tokenResult)
	token, _ := tokenResult["data"].(map[string]any)["token"].(string)
	if token == "" {
		return "", "", fmt.Errorf("empty token")
	}
	return appID, token, nil
}

// TestJWTAuth 测试 JWT 鉴权行为
func TestJWTAuth(t *testing.T) {
	agentID := fmt.Sprintf("test-auth-agent-%d", time.Now().UnixNano())
	invokeURL := "/gateway/invoke/agent/" + agentID

	_, validToken, err := createConsumerAndToken(nil)
	if err != nil {
		t.Fatalf("setup: %v", err)
	}

	// A1: 无 Authorization header → 401
	t.Run("A1_NoAuthHeader", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodPost, baseURL+invokeURL, bytes.NewBufferString(`{"input":{}}`))
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("expected 401, got %d", resp.StatusCode)
		}
	})

	// A2: 格式错误（非 Bearer）→ 401
	t.Run("A2_InvalidFormat", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodPost, baseURL+invokeURL, bytes.NewBufferString(`{"input":{}}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Token invalid-format")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("expected 401, got %d", resp.StatusCode)
		}
	})

	// A3: token 签名错误 → 401
	t.Run("A3_InvalidToken", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodPost, baseURL+invokeURL, bytes.NewBufferString(`{"input":{}}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer this.is.invalid")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("expected 401, got %d", resp.StatusCode)
		}
	})

	// A4: token 有效 → 通过鉴权（到达网关层，非 401/403）
	t.Run("A4_ValidToken", func(t *testing.T) {
		resp, err := postJSONWithToken(invokeURL, map[string]any{"input": map[string]any{}}, validToken)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer resp.Body.Close()
		// 鉴权通过后可能是 404（agent 不存在）或其他，但不应是 401/403
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			t.Fatalf("expected to pass auth, got %d", resp.StatusCode)
		}
	})
}

// TestAuthToken 测试 /auth/token 接口
func TestAuthToken(t *testing.T) {
	appID := fmt.Sprintf("test.token.%d", time.Now().UnixNano())
	secret := "test-secret-12345"

	// 注册 consumer
	t.Run("T1_RegisterConsumer", func(t *testing.T) {
		resp, err := postJSON("/register", map[string]string{
			"app_id":      appID,
			"secret":      secret,
			"description": "token test",
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

	// T2: 正确密码 → 200 + token
	t.Run("T2_ValidSecret", func(t *testing.T) {
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
		token, _ := result["data"].(map[string]any)["token"].(string)
		if token == "" {
			t.Fatal("token is empty")
		}
	})

	// T3: 错误密码 → 401
	t.Run("T3_InvalidSecret", func(t *testing.T) {
		resp, err := postJSON("/auth/token", map[string]string{
			"app_id": appID,
			"secret": "wrong-secret",
		})
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("expected 401, got %d", resp.StatusCode)
		}
	})

	// T4: 不存在的 app_id → 401
	t.Run("T4_UnknownAppID", func(t *testing.T) {
		resp, err := postJSON("/auth/token", map[string]string{
			"app_id": "not.exist.app",
			"secret": "any",
		})
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("expected 401, got %d", resp.StatusCode)
		}
	})
}
