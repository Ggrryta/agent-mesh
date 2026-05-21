package group

import (
	"context"
	"fmt"

	"go.uber.org/zap"
)

// AgentLookup 查询 agent 的 owner_uid（membership 资格校验用）。
type AgentLookup interface {
	Lookup(ctx context.Context, agentID string) (ownerUID int64, kind string, found bool)
}

// FriendshipChecker 查询两个 agent 是否已是好友（membership 资格校验用）。
type FriendshipChecker interface {
	AreFriends(ctx context.Context, a, b string) (bool, error)
}

// Service 群组业务逻辑。
type Service struct {
	repo        Repo
	agents      AgentLookup
	friends     FriendshipChecker
	cache       *Cache
	invalidator *Invalidator
	log         *zap.Logger
}

func NewService(repo Repo, log *zap.Logger) *Service {
	return &Service{repo: repo, log: log}
}

// WithCache 注入本地缓存 + 失效通知器。
func (s *Service) WithCache(cache *Cache, inv *Invalidator) *Service {
	s.cache = cache
	s.invalidator = inv
	return s
}

// WithEligibilityCheck 注入 agent/friendship 依赖，启用加成员时的资格校验：
// 被加成员的 owner 必须 = callerUID（加自己的 agent），
// 或被加成员是 callerUID 名下任一 agent 的好友。
func (s *Service) WithEligibilityCheck(agents AgentLookup, friends FriendshipChecker) *Service {
	s.agents = agents
	s.friends = friends
	return s
}

// Create 创建群组，创建者自动成为 owner 成员。
func (s *Service) Create(ctx context.Context, groupID, contextID, name string, ownerUID int64, creatorAgentID string) (*Group, error) {
	if groupID == "" {
		return nil, fmt.Errorf("group: group_id is required")
	}
	if contextID == "" {
		contextID = "ctx-" + groupID
	}

	g, err := s.repo.CreateGroup(ctx, &Group{
		GroupID:   groupID,
		ContextID: contextID,
		Name:      name,
		OwnerUID:  ownerUID,
	})
	if err != nil {
		return nil, err
	}

	// creatorAgentID 非空时自动加为 owner 成员；为空时只创建群组，用户后续手动加人
	if creatorAgentID != "" {
		if err := s.repo.AddMember(ctx, groupID, creatorAgentID, RoleOwner); err != nil {
			s.log.Warn("group: add creator as owner failed", zap.Error(err))
		}
	}
	return g, nil
}

// AddMember 添加成员。
// 资格规则（当注入了 AgentLookup + FriendshipChecker 时）：
//   - 调用方必须是群主
//   - 被加 agent 必须是调用方名下的 agent，或与调用方某个已在群内的 agent 是好友
//
// 未注入检查器时退化为只做群主鉴权。
func (s *Service) AddMember(ctx context.Context, groupID, agentID string, callerUID int64) error {
	g, err := s.repo.GetByGroupID(ctx, groupID)
	if err != nil {
		return err
	}
	if g.OwnerUID != callerUID {
		return ErrNotGroupOwner
	}

	if err := s.checkEligibility(ctx, groupID, agentID, callerUID); err != nil {
		return err
	}
	if err := s.repo.AddMember(ctx, groupID, agentID, RoleMember); err != nil {
		return err
	}
	if s.invalidator != nil {
		s.invalidator.PublishAgent(ctx, agentID)
	}
	return nil
}

// checkEligibility 检查被加成员是否有资格加入该群。
// 资格检查只在注入了 AgentLookup + FriendshipChecker 时启用。
func (s *Service) checkEligibility(ctx context.Context, groupID, agentID string, callerUID int64) error {
	if s.agents == nil || s.friends == nil {
		return nil // 未装配检查器，跳过（向后兼容）
	}

	targetOwner, _, ok := s.agents.Lookup(ctx, agentID)
	if !ok {
		return ErrAgentNotAllowed
	}
	// Case 1：被加 agent 是调用方自己的 agent。
	if targetOwner == callerUID {
		return nil
	}
	// Case 2：被加 agent 与调用方在群内的某个 agent 互为好友。
	members, err := s.repo.ListMembers(ctx, groupID)
	if err != nil {
		return err
	}
	for _, m := range members {
		ownerUID, _, ok := s.agents.Lookup(ctx, m.AgentID)
		if !ok || ownerUID != callerUID {
			continue
		}
		areFriends, err := s.friends.AreFriends(ctx, m.AgentID, agentID)
		if err == nil && areFriends {
			return nil
		}
	}
	return ErrAgentNotAllowed
}

// RemoveMember 移除成员。
func (s *Service) RemoveMember(ctx context.Context, groupID, agentID string, callerUID int64) error {
	g, err := s.repo.GetByGroupID(ctx, groupID)
	if err != nil {
		return err
	}
	if g.OwnerUID != callerUID {
		return ErrNotGroupOwner
	}
	if err := s.repo.RemoveMember(ctx, groupID, agentID); err != nil {
		return err
	}
	if s.invalidator != nil {
		s.invalidator.PublishAgent(ctx, agentID)
	}
	return nil
}

// ListMembers 列出群成员。
func (s *Service) ListMembers(ctx context.Context, groupID string) ([]*Member, error) {
	return s.repo.ListMembers(ctx, groupID)
}

// Get 获取群组信息。
func (s *Service) Get(ctx context.Context, groupID string) (*Group, error) {
	return s.repo.GetByGroupID(ctx, groupID)
}

// ListByOwner 列出用户名下所有群组（owner 视角）。
func (s *Service) ListByOwner(ctx context.Context, ownerUID int64) ([]*Group, error) {
	return s.repo.ListByOwner(ctx, ownerUID)
}

// IsMember 检查 agent 是否是群成员。
func (s *Service) IsMember(ctx context.Context, groupID, agentID string) (bool, error) {
	return s.repo.IsMember(ctx, groupID, agentID)
}

// SameGroup 判断两个 agent 是否在同一群组（鉴权用）。
func (s *Service) SameGroup(ctx context.Context, a, b string) (bool, error) {
	key := PairKey(a, b)
	if s.cache != nil {
		if result, hit := s.cache.Get(key); hit {
			return result, nil
		}
	}
	result, err := s.repo.SameGroup(ctx, a, b)
	if err != nil {
		return false, err
	}
	if s.cache != nil {
		s.cache.Set(key, result)
	}
	return result, nil
}

// GroupsOfAgent 返回 agent 参与的所有群组 ID（inbox fan-out 用）。
func (s *Service) GroupsOfAgent(ctx context.Context, agentID string) ([]string, error) {
	return s.repo.GroupsOfAgent(ctx, agentID)
}

// MembersOfGroupsContaining 返回所有与 agent 共处同一群组的其他 agent ID（去重，不含 agent 自己）。
// 用于 task 服务的 timeline fan-out。
func (s *Service) MembersOfGroupsContaining(ctx context.Context, agentID string) ([]string, error) {
	groups, err := s.repo.GroupsOfAgent(ctx, agentID)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{agentID: true}
	out := make([]string, 0)
	for _, gid := range groups {
		members, err := s.repo.ListMembers(ctx, gid)
		if err != nil {
			continue
		}
		for _, m := range members {
			if !seen[m.AgentID] {
				seen[m.AgentID] = true
				out = append(out, m.AgentID)
			}
		}
	}
	return out, nil
}
