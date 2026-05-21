package group

import (
	"context"
	"database/sql"
	"time"
)

// SQLRepo 是 MySQL 实现。
type SQLRepo struct{ db *sql.DB }

func NewSQLRepo(db *sql.DB) *SQLRepo { return &SQLRepo{db: db} }

func (r *SQLRepo) CreateGroup(ctx context.Context, g *Group) (*Group, error) {
	res, err := r.db.ExecContext(ctx,
		`INSERT INTO `+"`groups`"+` (group_id, context_id, name, owner_uid) VALUES (?, ?, ?, ?)`,
		g.GroupID, g.ContextID, g.Name, g.OwnerUID)
	if err != nil {
		if isDup(err) {
			return nil, ErrGroupIDExists
		}
		return nil, err
	}
	id, _ := res.LastInsertId()
	g.ID = id
	g.CreatedAt = time.Now()
	return g, nil
}

func (r *SQLRepo) GetByGroupID(ctx context.Context, groupID string) (*Group, error) {
	row := r.db.QueryRowContext(ctx,
		"SELECT id, group_id, context_id, name, owner_uid, created_at FROM `groups` WHERE group_id = ?", groupID)
	var g Group
	if err := row.Scan(&g.ID, &g.GroupID, &g.ContextID, &g.Name, &g.OwnerUID, &g.CreatedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, ErrGroupNotFound
		}
		return nil, err
	}
	return &g, nil
}

func (r *SQLRepo) ListByOwner(ctx context.Context, ownerUID int64) ([]*Group, error) {
	rows, err := r.db.QueryContext(ctx,
		"SELECT id, group_id, context_id, name, owner_uid, created_at FROM `groups` WHERE owner_uid = ? ORDER BY id", ownerUID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Group
	for rows.Next() {
		var g Group
		if err := rows.Scan(&g.ID, &g.GroupID, &g.ContextID, &g.Name, &g.OwnerUID, &g.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, &g)
	}
	return out, rows.Err()
}

func (r *SQLRepo) AddMember(ctx context.Context, groupID, agentID string, role MemberRole) error {
	_, err := r.db.ExecContext(ctx,
		"INSERT INTO group_members (group_id, agent_id, role) VALUES (?, ?, ?)",
		groupID, agentID, string(role))
	if err != nil {
		if isDup(err) {
			return ErrAlreadyMember
		}
		return err
	}
	return nil
}

func (r *SQLRepo) RemoveMember(ctx context.Context, groupID, agentID string) error {
	res, err := r.db.ExecContext(ctx,
		"DELETE FROM group_members WHERE group_id = ? AND agent_id = ?", groupID, agentID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotMember
	}
	return nil
}

func (r *SQLRepo) ListMembers(ctx context.Context, groupID string) ([]*Member, error) {
	rows, err := r.db.QueryContext(ctx,
		"SELECT id, group_id, agent_id, role, joined_at FROM group_members WHERE group_id = ? ORDER BY id", groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Member
	for rows.Next() {
		var m Member
		if err := rows.Scan(&m.ID, &m.GroupID, &m.AgentID, &m.Role, &m.JoinedAt); err != nil {
			return nil, err
		}
		out = append(out, &m)
	}
	return out, rows.Err()
}

func (r *SQLRepo) IsMember(ctx context.Context, groupID, agentID string) (bool, error) {
	var count int
	err := r.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM group_members WHERE group_id = ? AND agent_id = ?", groupID, agentID).Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

func isDup(err error) bool {
	return err != nil && (contains(err.Error(), "Duplicate entry") || contains(err.Error(), "UNIQUE constraint"))
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && containsImpl(s, sub))
}

func containsImpl(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// SameGroup 判断两个 agent 是否至少共享一个群组。
func (r *SQLRepo) SameGroup(ctx context.Context, a, b string) (bool, error) {
	if a == "" || b == "" || a == b {
		return false, nil
	}
	var count int
	err := r.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM group_members m1
		INNER JOIN group_members m2 ON m1.group_id = m2.group_id
		WHERE m1.agent_id = ? AND m2.agent_id = ?`, a, b).Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// GroupsOfAgent 返回 agent 参与的所有 group_id。
func (r *SQLRepo) GroupsOfAgent(ctx context.Context, agentID string) ([]string, error) {
	rows, err := r.db.QueryContext(ctx,
		"SELECT group_id FROM group_members WHERE agent_id = ?", agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var g string
		if err := rows.Scan(&g); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}
