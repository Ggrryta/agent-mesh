package group

import (
	"context"
	"testing"

	"go.uber.org/zap"
)

type memGroupRepo struct {
	groups  map[string]*Group
	members map[string][]*Member
}

func newMemRepo() *memGroupRepo {
	return &memGroupRepo{
		groups:  map[string]*Group{},
		members: map[string][]*Member{},
	}
}

func (r *memGroupRepo) CreateGroup(_ context.Context, g *Group) (*Group, error) {
	if _, ok := r.groups[g.GroupID]; ok {
		return nil, ErrGroupIDExists
	}
	cp := *g
	cp.ID = int64(len(r.groups) + 1)
	r.groups[g.GroupID] = &cp
	return &cp, nil
}

func (r *memGroupRepo) GetByGroupID(_ context.Context, groupID string) (*Group, error) {
	g, ok := r.groups[groupID]
	if !ok {
		return nil, ErrGroupNotFound
	}
	return g, nil
}

func (r *memGroupRepo) ListByOwner(_ context.Context, ownerUID int64) ([]*Group, error) {
	var out []*Group
	for _, g := range r.groups {
		if g.OwnerUID == ownerUID {
			out = append(out, g)
		}
	}
	return out, nil
}

func (r *memGroupRepo) AddMember(_ context.Context, groupID, agentID string, role MemberRole) error {
	for _, m := range r.members[groupID] {
		if m.AgentID == agentID {
			return ErrAlreadyMember
		}
	}
	r.members[groupID] = append(r.members[groupID], &Member{
		GroupID: groupID, AgentID: agentID, Role: role,
	})
	return nil
}

func (r *memGroupRepo) RemoveMember(_ context.Context, groupID, agentID string) error {
	list := r.members[groupID]
	for i, m := range list {
		if m.AgentID == agentID {
			r.members[groupID] = append(list[:i], list[i+1:]...)
			return nil
		}
	}
	return ErrNotMember
}

func (r *memGroupRepo) ListMembers(_ context.Context, groupID string) ([]*Member, error) {
	return r.members[groupID], nil
}

func (r *memGroupRepo) IsMember(_ context.Context, groupID, agentID string) (bool, error) {
	for _, m := range r.members[groupID] {
		if m.AgentID == agentID {
			return true, nil
		}
	}
	return false, nil
}

func (r *memGroupRepo) SameGroup(_ context.Context, a, b string) (bool, error) {
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

func TestService_CreateAndAddMembers(t *testing.T) {
	svc := NewService(newMemRepo(), zap.NewNop())
	ctx := context.Background()

	g, err := svc.Create(ctx, "grp-1", "", "Test Group", 1, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if g.GroupID != "grp-1" || g.ContextID != "ctx-grp-1" {
		t.Fatalf("unexpected: %+v", g)
	}

	if err := svc.AddMember(ctx, "grp-1", "bob", 1); err != nil {
		t.Fatal(err)
	}

	members, _ := svc.ListMembers(ctx, "grp-1")
	if len(members) != 2 {
		t.Fatalf("want 2 members (alice+bob), got %d", len(members))
	}

	ok, _ := svc.IsMember(ctx, "grp-1", "bob")
	if !ok {
		t.Fatal("bob should be member")
	}
}

func TestService_RemoveMember(t *testing.T) {
	svc := NewService(newMemRepo(), zap.NewNop())
	ctx := context.Background()

	svc.Create(ctx, "grp-1", "", "G", 1, "alice")
	svc.AddMember(ctx, "grp-1", "bob", 1)
	svc.RemoveMember(ctx, "grp-1", "bob", 1)

	ok, _ := svc.IsMember(ctx, "grp-1", "bob")
	if ok {
		t.Fatal("bob should be removed")
	}
}

func TestService_OnlyOwnerCanManage(t *testing.T) {
	svc := NewService(newMemRepo(), zap.NewNop())
	ctx := context.Background()

	svc.Create(ctx, "grp-1", "", "G", 1, "alice")

	err := svc.AddMember(ctx, "grp-1", "charlie", 99)
	if err != ErrNotGroupOwner {
		t.Fatalf("want ErrNotGroupOwner, got %v", err)
	}
}
