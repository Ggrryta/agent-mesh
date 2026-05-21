package group

import "context"

// Repo 群组数据访问接口。
type Repo interface {
	CreateGroup(ctx context.Context, g *Group) (*Group, error)
	GetByGroupID(ctx context.Context, groupID string) (*Group, error)
	ListByOwner(ctx context.Context, ownerUID int64) ([]*Group, error)

	AddMember(ctx context.Context, groupID, agentID string, role MemberRole) error
	RemoveMember(ctx context.Context, groupID, agentID string) error
	ListMembers(ctx context.Context, groupID string) ([]*Member, error)
	IsMember(ctx context.Context, groupID, agentID string) (bool, error)

	// SameGroup 返回两个 agent 是否至少共享一个群组。
	SameGroup(ctx context.Context, a, b string) (bool, error)

	// GroupsOfAgent 返回 agent 参与的所有群组 ID。
	GroupsOfAgent(ctx context.Context, agentID string) ([]string, error)
}
