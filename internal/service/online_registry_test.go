package service

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func newOnlineTestRedis(t *testing.T) (*redis.Client, *miniredis.Miniredis) {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	t.Cleanup(mr.Close)
	return redis.NewClient(&redis.Options{Addr: mr.Addr()}), mr
}

func TestOnlineRegistry_OnlineHeartbeatOffline(t *testing.T) {
	rdb, _ := newOnlineTestRedis(t)
	reg := NewOnlineRegistry(rdb, 5*time.Second)
	ctx := context.Background()

	err := reg.Online(ctx, AgentOnlineInfo{AgentID: "alice", GASInstanceID: "gas-1", IP: "1.1.1.1"})
	if err != nil {
		t.Fatalf("Online: %v", err)
	}
	ok, _ := reg.IsOnline(ctx, "alice")
	if !ok {
		t.Fatalf("should be online")
	}

	info, err := reg.GetInfo(ctx, "alice")
	if err != nil {
		t.Fatalf("GetInfo: %v", err)
	}
	if info == nil || info.GASInstanceID != "gas-1" || info.IP != "1.1.1.1" {
		t.Fatalf("bad info: %+v", info)
	}

	// 心跳续约
	if err := reg.Heartbeat(ctx, "alice", "gas-1"); err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}

	// Offline
	if err := reg.Offline(ctx, "alice", "gas-1"); err != nil {
		t.Fatalf("Offline: %v", err)
	}
	ok, _ = reg.IsOnline(ctx, "alice")
	if ok {
		t.Fatalf("should be offline")
	}
}

func TestOnlineRegistry_ConflictDifferentInstance(t *testing.T) {
	rdb, _ := newOnlineTestRedis(t)
	reg := NewOnlineRegistry(rdb, 5*time.Second)
	ctx := context.Background()

	_ = reg.Online(ctx, AgentOnlineInfo{AgentID: "alice", GASInstanceID: "gas-1"})
	// 另一实例尝试同一 agent_id 上线
	err := reg.Online(ctx, AgentOnlineInfo{AgentID: "alice", GASInstanceID: "gas-2"})
	if err != ErrAgentConflict {
		t.Fatalf("expected ErrAgentConflict, got %v", err)
	}

	// 心跳来自错实例也应失败
	if err := reg.Heartbeat(ctx, "alice", "gas-2"); err != ErrAgentConflict {
		t.Fatalf("heartbeat should fail, got %v", err)
	}
}

func TestOnlineRegistry_SameInstanceReOnline(t *testing.T) {
	rdb, _ := newOnlineTestRedis(t)
	reg := NewOnlineRegistry(rdb, 5*time.Second)
	ctx := context.Background()

	_ = reg.Online(ctx, AgentOnlineInfo{AgentID: "alice", GASInstanceID: "gas-1"})
	// 同实例重复 online = 续约
	if err := reg.Online(ctx, AgentOnlineInfo{AgentID: "alice", GASInstanceID: "gas-1"}); err != nil {
		t.Fatalf("same instance re-online should succeed: %v", err)
	}
}

func TestOnlineRegistry_HeartbeatMissingRecord(t *testing.T) {
	rdb, _ := newOnlineTestRedis(t)
	reg := NewOnlineRegistry(rdb, 5*time.Second)
	ctx := context.Background()

	// 无记录直接心跳
	if err := reg.Heartbeat(ctx, "alice", "gas-1"); err != ErrAgentConflict {
		t.Fatalf("expected ErrAgentConflict, got %v", err)
	}
}

func TestOnlineRegistry_TTLExpiry(t *testing.T) {
	rdb, mr := newOnlineTestRedis(t)
	reg := NewOnlineRegistry(rdb, 5*time.Second)
	ctx := context.Background()

	_ = reg.Online(ctx, AgentOnlineInfo{AgentID: "alice", GASInstanceID: "gas-1"})
	// miniredis 支持 FastForward 时间
	mr.FastForward(6 * time.Second)
	ok, _ := reg.IsOnline(ctx, "alice")
	if ok {
		t.Fatalf("should be offline after TTL")
	}
}
