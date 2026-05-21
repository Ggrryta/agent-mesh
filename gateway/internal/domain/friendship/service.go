package friendship

import (
	"context"
	"errors"
	"strings"
)

// AgentLookup 是 service 所需的 agent 视图。
// 用接口注入规避对 domain/agent 的直接依赖（保留单向 domain 图）。
type AgentLookup interface {
	// Lookup 按 agent_id 返回 (ownerUID, kind, found)。
	// kind 目前只区分 "normal" / "virtual-user"，使用字符串避免 import domain/agent。
	Lookup(ctx context.Context, agentID string) (ownerUID int64, kind string, found bool)
}

// virtualUserPrefix 与 domain/user.VirtualAgentIDFor 对齐。
// 兜底判断；主要仍走 AgentLookup.kind。
const virtualUserPrefix = "virtual-user-"

// Service 封装 friendship 的全部业务规则。
type Service struct {
	repo        Repo
	agents      AgentLookup
	cache       *Cache
	invalidator *Invalidator
}

// NewService 注入依赖。agents 不能为 nil —— 所有规则都依赖 agent 元数据。
func NewService(repo Repo, agents AgentLookup) *Service {
	if agents == nil {
		panic("friendship: AgentLookup is required")
	}
	return &Service{repo: repo, agents: agents}
}

// WithCache 注入本地缓存 + 失效通知器。可选：不调用则走 DB 直查（向后兼容）。
func (s *Service) WithCache(cache *Cache, inv *Invalidator) *Service {
	s.cache = cache
	s.invalidator = inv
	return s
}

// ── 写入路径 ────────────────────────────────────────────────────────────

// Request 发起一次好友请求。callerUID 是当前 Admin 登录的用户。
//
// 规则（详见 ADR 008）：
//   - fromAgentID 必须属于 callerUID（owner 只能以自己 agent 名义发起）
//   - from / to 都必须是 kind='normal' 的 agent；virtual-user 不参与
//   - from != to
//   - 若 (from, to) 不存在：INSERT 一行 pending
//   - 若已存在：
//     pending  → ErrAlreadyPending  （409）
//     accepted → ErrAlreadyAccepted （409，需先 Revoke 才能再发）
//     rejected → UPDATE 回 pending，覆盖 reason
//     revoked  → UPDATE 回 pending，覆盖 reason
func (s *Service) Request(ctx context.Context, callerUID int64, fromAgentID, toAgentID, reason string) (*Friendship, error) {
	from, to, err := s.normalizePair(fromAgentID, toAgentID)
	if err != nil {
		return nil, err
	}

	// 校验 from agent 归属 + kind。
	fromOwner, fromKind, ok := s.agents.Lookup(ctx, from)
	if !ok {
		return nil, ErrInvalidAgent
	}
	if fromOwner != callerUID {
		return nil, ErrNotOwner
	}
	if isVirtual(fromKind, from) {
		return nil, ErrVirtualUserPeer
	}

	// 校验 to agent 存在 + kind。
	_, toKind, ok := s.agents.Lookup(ctx, to)
	if !ok {
		return nil, ErrInvalidAgent
	}
	if isVirtual(toKind, to) {
		return nil, ErrVirtualUserPeer
	}

	// 走 GetByPair → 有则按状态分支；没有则 INSERT。
	existing, err := s.repo.GetByPair(ctx, from, to)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	if existing != nil {
		switch existing.Status {
		case StatusPending:
			return nil, ErrAlreadyPending
		case StatusAccepted:
			return nil, ErrAlreadyAccepted
		case StatusRejected, StatusRevoked:
			updated, err := s.repo.UpdateToPending(ctx, existing.ID, reason)
			if err != nil {
				return nil, err
			}
			if !updated {
				// 并发情况下状态可能已被改走（如 Accept）。重新查一次返回当前真相。
				return s.repo.GetByID(ctx, existing.ID)
			}
			return s.repo.GetByID(ctx, existing.ID)
		default:
			return nil, ErrInvalidTransition
		}
	}

	return s.repo.Insert(ctx, from, to, reason)
}

// Accept 接收方 owner 接受一个 pending 请求。
// 权限：callerUID 必须是 to_agent_id 的 owner。
func (s *Service) Accept(ctx context.Context, callerUID int64, id int64) (*Friendship, error) {
	return s.transition(ctx, callerUID, id, transitOp{
		from: StatusPending, to: StatusAccepted,
		requireOwnerOf: receiverSide,
	})
}

// Reject 接收方 owner 拒绝一个 pending 请求。保留行以便将来再 Request。
func (s *Service) Reject(ctx context.Context, callerUID int64, id int64) (*Friendship, error) {
	return s.transition(ctx, callerUID, id, transitOp{
		from: StatusPending, to: StatusRejected,
		requireOwnerOf: receiverSide,
	})
}

// Revoke 撤销一个 accepted 关系。from 或 to 任一方的 owner 都可以撤销。
func (s *Service) Revoke(ctx context.Context, callerUID int64, id int64) (*Friendship, error) {
	return s.transition(ctx, callerUID, id, transitOp{
		from: StatusAccepted, to: StatusRevoked,
		requireOwnerOf: anySide,
	})
}

// ── 查询路径 ────────────────────────────────────────────────────────────

// ListFriends 返回 agentID 名下的 friendships，调用方需证明自己是 owner。
// status 为空串时不过滤，否则只返回匹配的。
func (s *Service) ListFriends(ctx context.Context, callerUID int64, agentID string, status Status) ([]*Friendship, error) {
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		return nil, ErrInvalidAgent
	}
	owner, _, ok := s.agents.Lookup(ctx, agentID)
	if !ok {
		return nil, ErrInvalidAgent
	}
	if owner != callerUID {
		return nil, ErrNotOwner
	}
	all, err := s.repo.ListInvolvingAgent(ctx, agentID)
	if err != nil {
		return nil, err
	}
	if status == "" {
		return all, nil
	}
	out := make([]*Friendship, 0, len(all))
	for _, f := range all {
		if f.Status == status {
			out = append(out, f)
		}
	}
	return out, nil
}

// ListIncomingPending 返回"别人请求要加我这个 agent"的 pending 行。
// UI 的"待处理"入口用这个。
func (s *Service) ListIncomingPending(ctx context.Context, callerUID int64, agentID string) ([]*Friendship, error) {
	agentID = strings.TrimSpace(agentID)
	owner, _, ok := s.agents.Lookup(ctx, agentID)
	if !ok {
		return nil, ErrInvalidAgent
	}
	if owner != callerUID {
		return nil, ErrNotOwner
	}
	return s.repo.ListIncomingPending(ctx, agentID)
}

// AreFriends 报告两个 agent 当前是否可互发消息。给 Task 域（Week 3）用。
//
// 规则：
//   - 任一端是对方 owner 的 virtual-user → true（用户对自己 agent 免验证）
//   - 其它：查 friendships 是否有 accepted 行（任一方向）
//
// 查询路径尽量走一次 Lookup + 一次索引查询；热点场景由上层加 cache（Week 5）。
func (s *Service) AreFriends(ctx context.Context, a, b string) (bool, error) {
	if a == "" || b == "" {
		return false, nil
	}
	if a == b {
		return false, nil
	}

	// 隐式好友：virtual-user-{U} ↔ U 名下任何 normal agent。
	if implicit, err := s.implicitFriendCheck(ctx, a, b); err != nil || implicit {
		return implicit, err
	}

	// 本地缓存
	key := PairKey(a, b)
	if s.cache != nil {
		if allowed, hit := s.cache.Get(key); hit {
			return allowed, nil
		}
	}

	// DB 查询
	allowed, err := s.repo.ExistsAccepted(ctx, a, b)
	if err != nil {
		return false, err
	}

	// 回填缓存
	if s.cache != nil {
		s.cache.Set(key, allowed)
	}
	return allowed, nil
}

// implicitFriendCheck 判断 (a, b) 是否是"virtual-user 和其 owner 的 normal agent"对子。
// 两端都查一次 Lookup；一次 ownership 配对即可判定。
func (s *Service) implicitFriendCheck(ctx context.Context, a, b string) (bool, error) {
	aOwner, aKind, aOK := s.agents.Lookup(ctx, a)
	if !aOK {
		return false, nil
	}
	bOwner, bKind, bOK := s.agents.Lookup(ctx, b)
	if !bOK {
		return false, nil
	}
	// 必须是"一个 virtual-user，一个 normal"的组合，且同 owner。
	aVirtual := isVirtual(aKind, a)
	bVirtual := isVirtual(bKind, b)
	if aVirtual == bVirtual {
		return false, nil // 两个都 virtual 或都 normal 不走隐式
	}
	return aOwner == bOwner, nil
}

// ── 内部 helpers ────────────────────────────────────────────────────────

type transitOp struct {
	from, to       Status
	requireOwnerOf ownerSide
}

type ownerSide int

const (
	receiverSide ownerSide = iota // 必须是 to 的 owner
	anySide                       // from 或 to 的 owner 都可
)

// transition 是 Accept / Reject / Revoke 的公共路径。
func (s *Service) transition(ctx context.Context, callerUID int64, id int64, op transitOp) (*Friendship, error) {
	f, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}

	if err := s.assertOwnerOnSide(ctx, callerUID, f, op.requireOwnerOf); err != nil {
		return nil, err
	}

	if f.Status != op.from {
		return nil, ErrInvalidTransition
	}

	updated, err := s.repo.UpdateStatus(ctx, id, op.from, op.to)
	if err != nil {
		return nil, err
	}
	if !updated {
		return s.repo.GetByID(ctx, id)
	}

	// 关系变更后广播失效通知
	if s.invalidator != nil {
		s.invalidator.Publish(ctx, f.FromAgentID, f.ToAgentID)
	}

	return s.repo.GetByID(ctx, id)
}

func (s *Service) assertOwnerOnSide(ctx context.Context, callerUID int64, f *Friendship, side ownerSide) error {
	switch side {
	case receiverSide:
		owner, _, ok := s.agents.Lookup(ctx, f.ToAgentID)
		if !ok || owner != callerUID {
			return ErrNotOwner
		}
	case anySide:
		fromOwner, _, fromOK := s.agents.Lookup(ctx, f.FromAgentID)
		toOwner, _, toOK := s.agents.Lookup(ctx, f.ToAgentID)
		if (!fromOK || fromOwner != callerUID) && (!toOK || toOwner != callerUID) {
			return ErrNotOwner
		}
	}
	return nil
}

// normalizePair 做基本的格式校验。
func (s *Service) normalizePair(from, to string) (string, string, error) {
	from = strings.TrimSpace(from)
	to = strings.TrimSpace(to)
	if from == "" || to == "" {
		return "", "", ErrInvalidAgent
	}
	if from == to {
		return "", "", ErrSelfFriend
	}
	return from, to, nil
}

// isVirtual 双重保险：优先用 AgentLookup 返回的 kind 字段；
// 兜底看 ID 前缀（AgentLookup 实现错时也不会误放 virtual-user 进来）。
func isVirtual(kind, agentID string) bool {
	if kind == "virtual-user" {
		return true
	}
	return strings.HasPrefix(agentID, virtualUserPrefix)
}
