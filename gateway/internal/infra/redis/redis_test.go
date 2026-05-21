package redis

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/Ggrryta/agent-mesh/gateway/config"
)

func testAddr() string { return os.Getenv("AGENT_MESH_TEST_REDIS_ADDR") }

func TestOpen_Empty(t *testing.T) {
	cfg := &config.Config{}
	if _, err := Open(context.Background(), cfg); err == nil {
		t.Fatal("expected error for empty addr")
	}
}

func TestOpen_Live(t *testing.T) {
	addr := testAddr()
	if addr == "" {
		t.Skip("AGENT_MESH_TEST_REDIS_ADDR not set; skipping live test")
	}
	cfg := &config.Config{RedisAddr: addr}
	c, err := Open(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer c.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := Checker(c)(ctx); err != nil {
		t.Fatalf("Checker: %v", err)
	}
}

func TestOpen_BadAddr(t *testing.T) {
	cfg := &config.Config{RedisAddr: "127.0.0.1:1"}
	if _, err := Open(context.Background(), cfg); err == nil {
		t.Fatal("expected connection failure")
	}
}
