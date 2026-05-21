package e2e

import (
	"context"
	"sync"

	"github.com/Ggrryta/agent-mesh/gateway/internal/domain/group"
)

// memGroupRepo 内存群组实现，用于 e2e 测试。
type memGroupRepo struct {
	mu      sync.Mutex
	groups  map[string]*group.Group
	members map[string][]*group.Member
	next    int64
}

func newMemGroupRepo() *memGroupRepo {
	return &memGroupRepo{
		groups:  map[string]*group.Group{},
		members: map[string][]*group.Member{},
	}
}

func (r *memGroupRepo) CreateGroup(_ context.Context, g *group.Group) (*group.Group, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.groups[g.GroupID]; ok {
		return nil, group.ErrGroupIDExists
	}
	r.next++
	cp := *g
	cp.ID = r.next
	r.groups[g.GroupID] = &cp
	return &cp, nil
}

func (r *memGroupRepo) GetByGroupID(_ context.Context, groupID string) (*group.Group, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	g, ok := r.groups[groupID]
	if !ok {
		return nil, group.ErrGroupNotFound
	}
	cp := *g
	return &cp, nil
}

func (r *memGroupRepo) ListByOwner(_ context.Context, ownerUID int64) ([]*group.Group, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*group.Group
	for _, g := range r.groups {
		if g.OwnerUID == ownerUID {
			cp := *g
			out = append(out, &cp)
		}
	}
	return out, nil
}

func (r *memGroupRepo) AddMember(_ context.Context, groupID, agentID string, role group.MemberRole) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, m := range r.members[groupID] {
		if m.AgentID == agentID {
			return group.ErrAlreadyMember
		}
	}
	r.members[groupID] = append(r.members[groupID], &group.Member{
		GroupID: groupID, AgentID: agentID, Role: role,
	})
	return nil
}

func (r *memGroupRepo) RemoveMember(_ context.Context, groupID, agentID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	list := r.members[groupID]
	for i, m := range list {
		if m.AgentID == agentID {
			r.members[groupID] = append(list[:i], list[i+1:]...)
			return nil
		}
	}
	return group.ErrNotMember
}

func (r *memGroupRepo) ListMembers(_ context.Context, groupID string) ([]*group.Member, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	list := r.members[groupID]
	out := make([]*group.Member, len(list))
	for i, m := range list {
		cp := *m
		out[i] = &cp
	}
	return out, nil
}

func (r *memGroupRepo) IsMember(_ context.Context, groupID, agentID string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, m := range r.members[groupID] {
		if m.AgentID == agentID {
			return true, nil
		}
	}
	return false, nil
}

func (r *memGroupRepo) SameGroup(_ context.Context, a, b string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, members := range r.members {
		var hasA, hasB bool
		for _, m := range members {
			if m.AgentID == a {
				hasA = true
			}
			if m.AgentID == b {
				hasB = true
			}
		}
		if hasA && hasB {
			return true, nil
		}
	}
	return false, nil
}

func (r *memGroupRepo) GroupsOfAgent(_ context.Context, agentID string) ([]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for gid, members := range r.members {
		for _, m := range members {
			if m.AgentID == agentID {
				out = append(out, gid)
				break
			}
		}
	}
	return out, nil
}
