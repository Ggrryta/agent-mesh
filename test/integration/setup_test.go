//go:build integration

package integration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"
)

const baseURL = "http://localhost:8080"

// testToken 是包级共享 token，由 TestMain 初始化
var testToken string

func TestMain(m *testing.M) {
	if err := waitReady(5); err != nil {
		fmt.Fprintf(os.Stderr, "server not ready: %v\n", err)
		os.Exit(1)
	}

	_, tok, err := createConsumerAndToken(nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "TestMain: create test token failed: %v\n", err)
		os.Exit(1)
	}
	testToken = tok

	os.Exit(m.Run())
}

func waitReady(maxSeconds int) error {
	deadline := time.Now().Add(time.Duration(maxSeconds) * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(baseURL + "/health/ready")
		if err == nil && resp.StatusCode == http.StatusOK {
			resp.Body.Close()
			return nil
		}
		if resp != nil {
			resp.Body.Close()
		}
		time.Sleep(300 * time.Millisecond)
	}
	return fmt.Errorf("timeout after %ds", maxSeconds)
}

func postJSON(path string, payload any) (*http.Response, error) {
	body, _ := json.Marshal(payload)
	req, err := http.NewRequest(http.MethodPost, baseURL+path, bytes.NewBuffer(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	return http.DefaultClient.Do(req)
}

func getWithToken(path string, token string) (*http.Response, error) {
	req, err := http.NewRequest(http.MethodGet, baseURL+path, nil)
	if err != nil {
		return nil, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return http.DefaultClient.Do(req)
}

func deleteReq(path string) {
	req, _ := http.NewRequest(http.MethodDelete, baseURL+path, nil)
	resp, _ := http.DefaultClient.Do(req)
	if resp != nil {
		resp.Body.Close()
	}
}

func deleteReqWithToken(path string, token string) {
	req, _ := http.NewRequest(http.MethodDelete, baseURL+path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, _ := http.DefaultClient.Do(req)
	if resp != nil {
		resp.Body.Close()
	}
}

