package group

import (
	"errors"
	"time"
)

var (
	ErrGroupNotFound   = errors.New("group: not found")
	ErrAlreadyMember   = errors.New("group: already a member")
	ErrNotMember       = errors.New("group: not a member")
	ErrNotGroupOwner   = errors.New("group: not the owner")
	ErrGroupIDExists   = errors.New("group: group_id already exists")
	ErrAgentNotAllowed = errors.New("group: target agent must be your own or your friend")
)

type MemberRole string

const (
	RoleOwner  MemberRole = "owner"
	RoleAdmin  MemberRole = "admin"
	RoleMember MemberRole = "member"
)

type Group struct {
	ID        int64
	GroupID   string
	ContextID string
	Name      string
	OwnerUID  int64
	CreatedAt time.Time
}

type Member struct {
	ID       int64
	GroupID  string
	AgentID  string
	Role     MemberRole
	JoinedAt time.Time
}
