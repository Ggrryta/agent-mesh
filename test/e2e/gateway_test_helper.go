//go:build e2e
// +build e2e

// Package e2e 提供 M7 端到端测试用的精简 Gateway
package e2e

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"agent-gateway/test/e2e/minigwlib"
)

type GatewayTestEnv struct {
	BaseURL string
	Inst    *minigwlib.Instance
}

func StartGateway(t *testing.T) *GatewayTestEnv {
	t.Helper()
	inst, err := minigwlib.Start("")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	env := &GatewayTestEnv{
		BaseURL: "http://" + inst.Addr,
		Inst:    inst,
	}
	if err := env.waitReady(5 * time.Second); err != nil {
		t.Fatalf("gateway not ready: %v", err)
	}
	return env
}

func (g *GatewayTestEnv) waitReady(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := http.Get(g.BaseURL + "/friendships/pending")
		if err == nil {
			resp.Body.Close()
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("timeout")
}

func (g *GatewayTestEnv) Stop() {
	ctx, c := context.WithTimeout(context.Background(), 2*time.Second)
	defer c()
	g.Inst.Stop(ctx)
}
