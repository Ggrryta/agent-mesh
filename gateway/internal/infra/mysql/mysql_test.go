package mysql

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Ggrryta/agent-mesh/gateway/config"
)

// testDSN 返回 dev harness 注入的 DSN。没设置时跳过测试，
// 这样没有 live MySQL 的 CI 也能通过。
func testDSN() string {
	if v := os.Getenv("AGENT_MESH_TEST_MYSQL_DSN"); v != "" {
		return v
	}
	// 本地 docker-compose.dev.yml 提供的默认值。
	return ""
}

func TestOpen_Empty(t *testing.T) {
	cfg := &config.Config{MySQLDSN: ""}
	if _, err := Open(context.Background(), cfg); err == nil {
		t.Fatal("expected error for empty DSN")
	}
}

func TestOpen_Live(t *testing.T) {
	dsn := testDSN()
	if dsn == "" {
		t.Skip("AGENT_MESH_TEST_MYSQL_DSN not set; skipping live test")
	}
	cfg := &config.Config{
		MySQLDSN:             dsn,
		MySQLMaxOpenConns:    5,
		MySQLMaxIdleConns:    2,
		MySQLConnMaxLifetime: time.Minute,
	}
	db, err := Open(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := db.Checker()(ctx); err != nil {
		t.Fatalf("Checker: %v", err)
	}
}

func TestOpen_BadDSN(t *testing.T) {
	cfg := &config.Config{
		MySQLDSN:             "mesh:wrong@tcp(127.0.0.1:1)/agent_mesh",
		MySQLMaxOpenConns:    5,
		MySQLMaxIdleConns:    2,
		MySQLConnMaxLifetime: time.Minute,
	}
	_, err := Open(context.Background(), cfg)
	if err == nil {
		t.Fatal("expected connection failure")
	}
	// Must be a ping-phase failure, not an Open parse error — that is the
	// class K8s readiness probes care about.
	if !strings.Contains(err.Error(), "ping") && !strings.Contains(err.Error(), "connect") {
		t.Fatalf("unexpected error kind: %v", err)
	}
}
