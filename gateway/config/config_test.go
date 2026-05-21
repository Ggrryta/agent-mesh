package config

import (
	"testing"
	"time"
)

func TestLoad_Defaults(t *testing.T) {
	// Empty env means unset; Load should return defaults without error.
	t.Setenv("GATEWAY_BUSINESS_ADDR", "")
	t.Setenv("GATEWAY_ADMIN_ADDR", "")
	t.Setenv("LOG_LEVEL", "")
	t.Setenv("LOG_FORMAT", "")
	t.Setenv("MYSQL_DSN", "")
	t.Setenv("REDIS_ADDR", "")
	t.Setenv("MYSQL_MAX_OPEN_CONNS", "")
	t.Setenv("MYSQL_MAX_IDLE_CONNS", "")
	t.Setenv("MYSQL_CONN_MAX_LIFETIME", "")
	t.Setenv("REDIS_DB", "")
	t.Setenv("SHUTDOWN_TIMEOUT", "")
	t.Setenv("STARTUP_READY_DELAY", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.BusinessAddr != ":8080" {
		t.Errorf("BusinessAddr default: want :8080, got %s", cfg.BusinessAddr)
	}
	if cfg.AdminAddr != ":9090" {
		t.Errorf("AdminAddr default: want :9090, got %s", cfg.AdminAddr)
	}
	if cfg.LogLevel != "info" {
		t.Errorf("LogLevel default: want info, got %s", cfg.LogLevel)
	}
	if cfg.LogFormat != "json" {
		t.Errorf("LogFormat default: want json, got %s", cfg.LogFormat)
	}
	if cfg.MySQLMaxOpenConns != 50 {
		t.Errorf("MySQLMaxOpenConns default: want 50, got %d", cfg.MySQLMaxOpenConns)
	}
	if cfg.ShutdownTimeout != 60*time.Second {
		t.Errorf("ShutdownTimeout default: want 60s, got %v", cfg.ShutdownTimeout)
	}
}

func TestLoad_EnvOverride(t *testing.T) {
	t.Setenv("GATEWAY_BUSINESS_ADDR", ":7777")
	t.Setenv("GATEWAY_ADMIN_ADDR", ":7778")
	t.Setenv("LOG_LEVEL", "debug")
	t.Setenv("LOG_FORMAT", "console")
	t.Setenv("MYSQL_DSN", "user:pass@tcp(host)/db")
	t.Setenv("REDIS_ADDR", "redis:6379")
	t.Setenv("MYSQL_MAX_OPEN_CONNS", "100")
	t.Setenv("SHUTDOWN_TIMEOUT", "15s")
	t.Setenv("POD_NAME", "pod-xyz")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.BusinessAddr != ":7777" {
		t.Errorf("BusinessAddr: want :7777, got %s", cfg.BusinessAddr)
	}
	if cfg.AdminAddr != ":7778" {
		t.Errorf("AdminAddr: want :7778, got %s", cfg.AdminAddr)
	}
	if cfg.LogLevel != "debug" {
		t.Errorf("LogLevel: want debug, got %s", cfg.LogLevel)
	}
	if cfg.MySQLMaxOpenConns != 100 {
		t.Errorf("MySQLMaxOpenConns: want 100, got %d", cfg.MySQLMaxOpenConns)
	}
	if cfg.ShutdownTimeout != 15*time.Second {
		t.Errorf("ShutdownTimeout: want 15s, got %v", cfg.ShutdownTimeout)
	}
	if cfg.PodName != "pod-xyz" {
		t.Errorf("PodName: want pod-xyz, got %s", cfg.PodName)
	}
}

func TestLoad_InvalidInt(t *testing.T) {
	t.Setenv("MYSQL_MAX_OPEN_CONNS", "not-a-number")
	_, err := Load()
	if err == nil {
		t.Fatal("expected error for invalid int")
	}
}

func TestLoad_InvalidDuration(t *testing.T) {
	t.Setenv("SHUTDOWN_TIMEOUT", "forever")
	_, err := Load()
	if err == nil {
		t.Fatal("expected error for invalid duration")
	}
}
