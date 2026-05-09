package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// AgentOnlineInfo 在线 agent 的运行时信息
type AgentOnlineInfo struct {
	AgentID       string    `json:"agent_id"`
	GASInstanceID string    `json:"gas_instance_id"` // 来自哪个 GAS 实例
	LastHeartbeat time.Time `json:"last_heartbeat"`
	IP            string    `json:"ip,omitempty"`
}

// OnlineRegistry 管理 agent 在线状态
// 基于 Redis HASH + EXPIRE 实现:
//
//	key:   online:agent:<agent_id>
//	value: hash { gas_instance_id, last_heartbeat(unix ms), ip }
//	TTL:   90s (每次心跳续期)
//
// 90s 无心跳自动过期 = 视为离线
type OnlineRegistry struct {
	rdb *redis.Client
	ttl time.Duration
}

var (
	// ErrAgentConflict 同一 agent_id 已在另一 GAS 实例上线
	ErrAgentConflict = errors.New("agent already online on another instance")
)

// NewOnlineRegistry 创建
// ttl 推荐 90s,配合 30s 心跳间隔能容忍两次心跳丢失
func NewOnlineRegistry(rdb *redis.Client, ttl time.Duration) *OnlineRegistry {
	if ttl <= 0 {
		ttl = 90 * time.Second
	}
	return &OnlineRegistry{rdb: rdb, ttl: ttl}
}

func onlineKey(agentID string) string {
	return "online:agent:" + agentID
}

// Online 标记 agent 上线。
// 若已有其他 GAS 实例上线,返回 ErrAgentConflict。
// 同一 GAS 实例重复 Online 视为续约,不冲突。
func (r *OnlineRegistry) Online(ctx context.Context, info AgentOnlineInfo) error {
	key := onlineKey(info.AgentID)
	// 先 GET 当前记录,判断是否冲突
	existed, err := r.rdb.HGet(ctx, key, "gas_instance_id").Result()
	if err != nil && err != redis.Nil {
		return fmt.Errorf("online get: %w", err)
	}
	if err == nil && existed != "" && existed != info.GASInstanceID {
		return ErrAgentConflict
	}

	now := time.Now()
	info.LastHeartbeat = now
	fields := map[string]any{
		"gas_instance_id": info.GASInstanceID,
		"last_heartbeat":  now.UnixMilli(),
		"ip":              info.IP,
	}
	if err := r.rdb.HSet(ctx, key, fields).Err(); err != nil {
		return fmt.Errorf("online hset: %w", err)
	}
	return r.rdb.Expire(ctx, key, r.ttl).Err()
}

// Heartbeat 心跳续约。agent_id 必须属于 gas_instance_id。
// 若不存在或不匹配,返回 ErrAgentConflict(可能是被别处抢占)。
func (r *OnlineRegistry) Heartbeat(ctx context.Context, agentID, gasInstanceID string) error {
	key := onlineKey(agentID)
	existed, err := r.rdb.HGet(ctx, key, "gas_instance_id").Result()
	if err != nil {
		if err == redis.Nil {
			return ErrAgentConflict
		}
		return err
	}
	if existed != gasInstanceID {
		return ErrAgentConflict
	}
	if err := r.rdb.HSet(ctx, key, "last_heartbeat", time.Now().UnixMilli()).Err(); err != nil {
		return err
	}
	return r.rdb.Expire(ctx, key, r.ttl).Err()
}

// Offline 主动下线
func (r *OnlineRegistry) Offline(ctx context.Context, agentID, gasInstanceID string) error {
	key := onlineKey(agentID)
	existed, err := r.rdb.HGet(ctx, key, "gas_instance_id").Result()
	if err != nil {
		if err == redis.Nil {
			return nil // 已经离线
		}
		return err
	}
	if existed != gasInstanceID {
		return ErrAgentConflict
	}
	return r.rdb.Del(ctx, key).Err()
}

// IsOnline 检查 agent 是否在线
func (r *OnlineRegistry) IsOnline(ctx context.Context, agentID string) (bool, error) {
	n, err := r.rdb.Exists(ctx, onlineKey(agentID)).Result()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// GetInfo 查询在线 agent 详细信息,离线返回 nil, nil
func (r *OnlineRegistry) GetInfo(ctx context.Context, agentID string) (*AgentOnlineInfo, error) {
	key := onlineKey(agentID)
	fields, err := r.rdb.HGetAll(ctx, key).Result()
	if err != nil {
		return nil, err
	}
	if len(fields) == 0 {
		return nil, nil
	}
	info := &AgentOnlineInfo{
		AgentID:       agentID,
		GASInstanceID: fields["gas_instance_id"],
		IP:            fields["ip"],
	}
	if ts := fields["last_heartbeat"]; ts != "" {
		var ms int64
		if _, err := fmt.Sscanf(ts, "%d", &ms); err == nil {
			info.LastHeartbeat = time.UnixMilli(ms)
		}
	}
	return info, nil
}
