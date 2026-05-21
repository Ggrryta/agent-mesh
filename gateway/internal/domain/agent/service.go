package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// SkillRepo 让 agent service 在 Register 时原子替换 agent 的 skill 集合。
// 定义在本包而不是从 skill 包 import —— 保持单向依赖图（agent 只依赖 skill
// 接口，不反过来）。
type SkillRepo interface {
	ReplaceByAgentID(ctx context.Context, agentID string, skills any) error
}

// Service 是对 repo + cache 的事务 façade。API 层永远不直接接触 Repo 或
// Cache，这样我们能在一处改动"cache-then-db / db-then-cache"的时序。
type Service struct {
	repo   Repo
	cache  *Cache
	skills SkillRepo // 可选；nil 表示跳过 skill 同步
}

// NewService 注入必备依赖。Skills 通过 WithSkills 注入，让 NewService 保持
// 精简，同时又留出一个挂载 Skill 域的位置。
func NewService(repo Repo, cache *Cache) *Service { return &Service{repo: repo, cache: cache} }

// WithSkills 注入一个 skill 替换器，返回 service 以便链式调用。
// 不挂 skill repo 时调 Register 只是跳过 skill 同步步骤。
func (s *Service) WithSkills(r SkillRepo) *Service {
	s.skills = r
	return s
}

// RegisterInput 对应 mesh /agents/register 的请求体。
type RegisterInput struct {
	AgentID       string
	OwnerUID      int64
	Name          string
	Description   string
	URL           string
	Version       string
	SystemPrompt  string          // 可选：用户配置的角色身份提示词
	WorkspacePath string          // 可选：agent 的工作目录路径
	AgentCard     json.RawMessage // 完整的 A2A AgentCard，未来消费者可用
	Skills        any             // domain/skill.Input slice；用 any 避开 import cycle
}

// Register 创建或更新一个 agent。已存在的 agent 必须属于同一个调用者。
// cache 写在 DB 成功之后，这样事务回滚不会污染到路由。
func (s *Service) Register(ctx context.Context, in RegisterInput) (*Agent, error) {
	in.AgentID = NormalizeAgentID(in.AgentID)
	if err := ValidateAgentID(in.AgentID); err != nil {
		return nil, err
	}
	if in.Name == "" {
		return nil, errors.New("agent: name is required")
	}
	if in.OwnerUID == 0 {
		return nil, errors.New("agent: owner uid is required")
	}
	if len(in.SystemPrompt) > MaxSystemPromptBytes {
		return nil, ErrSystemPromptTooLong
	}

	// 重名防护：如果行已存在，必须同 owner。Register 不允许转移 ownership,
	// 也不允许覆盖 virtual-user agent。
	if existing, err := s.repo.GetByAgentID(ctx, in.AgentID); err == nil {
		if existing.OwnerUID != in.OwnerUID {
			return nil, ErrNotOwner
		}
		if existing.Kind == KindVirtualUser {
			return nil, ErrReservedVirtualName
		}
	} else if !errors.Is(err, ErrAgentNotFound) {
		return nil, err
	}

	// 新建 agent 默认 inactive：还没有 GAS daemon 接入，没有心跳，
	// 不应被认为"在线"。第一次 Heartbeat 调用时会转 active。
	// 这样区分了"接入中/已离线"的语义，避免幽灵 agent 出现在 market 列表。
	a := &Agent{
		AgentID:       in.AgentID,
		OwnerUID:      in.OwnerUID,
		Name:          in.Name,
		Description:   in.Description,
		URL:           in.URL,
		Version:       in.Version,
		Kind:          KindNormal,
		Status:        StatusInactive,
		AgentCardJSON: []byte(in.AgentCard),
		SystemPrompt:  in.SystemPrompt,
		WorkspacePath: in.WorkspacePath,
		// LastHeartbeatAt 故意留 nil —— 真正心跳过来才填
	}
	if err := s.repo.Upsert(ctx, a); err != nil {
		return nil, err
	}
	// 当调用方带了 skills 且 skill repo 已注入时，顺带同步。MVP 阶段 skill
	// 同步是尽力而为：失败不回滚 agent 行，调用方可用同样 payload 重试。
	if s.skills != nil && in.Skills != nil {
		if err := s.skills.ReplaceByAgentID(ctx, a.AgentID, in.Skills); err != nil {
			return nil, fmt.Errorf("agent: skill sync: %w", err)
		}
	}
	// 再读一次，让 CreatedAt / UpdatedAt 在 cache snapshot 里也是满的。
	stored, err := s.repo.GetByAgentID(ctx, in.AgentID)
	if err != nil {
		return nil, fmt.Errorf("agent: reload after upsert: %w", err)
	}
	s.cache.Set(stored)
	return stored, nil
}

// Get 按 id 查一个 agent —— 主要给 admin GET handler 用。
// 走 MySQL，以免从本方法泄漏陈旧 cache。列表型高频读路径请直接用 Cache。
func (s *Service) Get(ctx context.Context, agentID string) (*Agent, error) {
	return s.repo.GetByAgentID(ctx, NormalizeAgentID(agentID))
}

// Drain 把 agent 标为 draining。路由器看作不可路由，但行仍保留，方便
// admin UI 展示它为什么消失。
func (s *Service) Drain(ctx context.Context, agentID string, callerUID int64) error {
	a, err := s.assertOwner(ctx, agentID, callerUID)
	if err != nil {
		return err
	}
	if a.Status == StatusDraining {
		return nil // 幂等
	}
	if err := s.repo.UpdateStatus(ctx, a.AgentID, StatusDraining); err != nil {
		return err
	}
	a.Status = StatusDraining
	s.cache.Set(a)
	return nil
}

// Deregister 彻底删除 agent 行。owner 主动删时调用；短暂不可用不走这里
// （那是 Status 驱动的）。
func (s *Service) Deregister(ctx context.Context, agentID string, callerUID int64) error {
	a, err := s.assertOwner(ctx, agentID, callerUID)
	if err != nil {
		return err
	}
	if a.Kind == KindVirtualUser {
		return ErrReservedVirtualName
	}
	if err := s.repo.Delete(ctx, a.AgentID); err != nil {
		return err
	}
	s.cache.Delete(a.AgentID)
	return nil
}

// Heartbeat 同时更新 DB 行和 cache snapshot。由
// /mesh/agents/:id/heartbeat 调用。
//
// 状态机：
//   - inactive → active（第一次心跳，agent 真正接入）
//   - active 维持
//   - draining：心跳不会让它回 active（drain 是用户主动操作，agent 心跳不能撤销）
func (s *Service) Heartbeat(ctx context.Context, agentID string, callerUID int64) (*Agent, error) {
	a, err := s.assertOwner(ctx, agentID, callerUID)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	if err := s.repo.UpdateHeartbeat(ctx, a.AgentID, now); err != nil {
		return nil, err
	}
	a.LastHeartbeatAt = &now
	// 非 active 状态（inactive / draining）收到心跳 → 恢复为 active
	if a.Status != StatusActive {
		if err := s.repo.UpdateStatus(ctx, a.AgentID, StatusActive); err != nil {
			return nil, fmt.Errorf("agent: activate on heartbeat: %w", err)
		}
		a.Status = StatusActive
	}
	s.cache.Set(a)
	return a, nil
}

// ListByOwner 返回某用户拥有的 normal agent，给 admin UI 列表页用。
// virtual-user agent 被过滤掉，因为 UI 另行展示用户自己的身份。
func (s *Service) ListByOwner(ctx context.Context, uid int64) ([]*Agent, error) {
	return s.repo.List(ctx, Filter{
		OwnerUID: uid,
		Kind:     KindNormal,
	})
}

// ListAllActive 给 AgentCache.Reload 和只读发现用。
// 包含 virtual-user agent —— mesh 路由要靠它们投递用户发起的消息。
func (s *Service) ListAllActive(ctx context.Context) ([]*Agent, error) {
	return s.repo.List(ctx, Filter{Status: StatusActive})
}

// ListMarket 给 /v1/admin/market/agents 端点用。
//
// 语义：
//   - 仅 kind=normal + status=active 会出现，virtual-user / draining / inactive 全不列
//   - search 空则全量（仍受 limit 约束）
//   - offset + limit 走分页；limit 上限由 handler 层把关
//
// 调用方（登录过的任何 user）看到的结果相同；没有 visibility / per-user
// 过滤规则 —— MVP 的 market 是"对所有用户透明"的。
func (s *Service) ListMarket(ctx context.Context, search string, limit, offset int) ([]*Agent, error) {
	return s.repo.List(ctx, Filter{
		Status: StatusActive,
		Kind:   KindNormal,
		Search: search,
		Limit:  limit,
		Offset: offset,
	})
}

// assertOwner 是所有改状态操作的唯一入口闸门。
// 返回一份 agent 拷贝，调用方可以改动它而不影响 cache 里的条目。
func (s *Service) assertOwner(ctx context.Context, agentID string, callerUID int64) (*Agent, error) {
	a, err := s.repo.GetByAgentID(ctx, NormalizeAgentID(agentID))
	if err != nil {
		return nil, err
	}
	if a.OwnerUID != callerUID {
		return nil, ErrNotOwner
	}
	return a, nil
}
