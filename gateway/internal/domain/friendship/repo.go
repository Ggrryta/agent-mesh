package friendship

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Repo 是数据访问接口。service 依赖它，不直接依赖 *sql.DB。
type Repo interface {
	// GetByPair 按 (from, to) 唯一索引查行。没有返回 ErrNotFound。
	GetByPair(ctx context.Context, from, to string) (*Friendship, error)
	// GetByID 按主键查，含 owner 校验时用。
	GetByID(ctx context.Context, id int64) (*Friendship, error)
	// Insert 新建 pending 行。uk_pair 冲突时返回错误，调用方应退回到 UPDATE 路径。
	Insert(ctx context.Context, from, to, reason string) (*Friendship, error)
	// UpdateToPending 把 (from, to) 行状态改回 pending 并覆盖 reason。
	// 只有当当前 status ∈ {rejected, revoked} 时才生效（RowsAffected 判定）。
	// 该方法不会把 accepted / pending 行拉回 pending。
	UpdateToPending(ctx context.Context, id int64, reason string) (bool, error)
	// UpdateStatus 原子切换状态，要求 fromStatus 匹配，避免并发下的旧状态写入。
	UpdateStatus(ctx context.Context, id int64, fromStatus, toStatus Status) (bool, error)

	// ListInvolvingAgent 返回某个 agent 作为 from 或 to 的所有 friendship。
	// UI 按 status 过滤 / 排序。
	ListInvolvingAgent(ctx context.Context, agentID string) ([]*Friendship, error)
	// ListIncomingPending 返回该 agent 作为接收方且 pending 的请求（用于 UI 的"待处理"）。
	ListIncomingPending(ctx context.Context, agentID string) ([]*Friendship, error)

	// ExistsAccepted：任一方向存在 accepted 行。给 AreFriends 短路用。
	ExistsAccepted(ctx context.Context, a, b string) (bool, error)
}

// SQLRepo 是 MySQL 实现。
type SQLRepo struct{ db *sql.DB }

func NewSQLRepo(db *sql.DB) *SQLRepo { return &SQLRepo{db: db} }

const selectCols = `
	SELECT id, from_agent_id, to_agent_id, status, reason, created_at, updated_at
	FROM friendships
`

func (r *SQLRepo) GetByPair(ctx context.Context, from, to string) (*Friendship, error) {
	row := r.db.QueryRowContext(ctx,
		selectCols+"WHERE from_agent_id = ? AND to_agent_id = ? LIMIT 1", from, to)
	return scanOne(row)
}

func (r *SQLRepo) GetByID(ctx context.Context, id int64) (*Friendship, error) {
	row := r.db.QueryRowContext(ctx, selectCols+"WHERE id = ? LIMIT 1", id)
	return scanOne(row)
}

// Insert 创建一行 pending friendship。uk_pair 冲突时返回原生错误，
// 交给 service 层决定 "转 UPDATE" 还是 "409 already pending"。
func (r *SQLRepo) Insert(ctx context.Context, from, to, reason string) (*Friendship, error) {
	res, err := r.db.ExecContext(ctx, `
		INSERT INTO friendships (from_agent_id, to_agent_id, status, reason)
		VALUES (?, ?, ?, ?)`, from, to, string(StatusPending), reason)
	if err != nil {
		return nil, fmt.Errorf("friendship: insert: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("friendship: last id: %w", err)
	}
	return r.GetByID(ctx, id)
}

// UpdateToPending 只在当前状态 ∈ {rejected, revoked} 时把行拉回 pending。
// 返回值 updated 指示是否真的改到。
func (r *SQLRepo) UpdateToPending(ctx context.Context, id int64, reason string) (bool, error) {
	res, err := r.db.ExecContext(ctx, `
		UPDATE friendships
		SET status = ?, reason = ?
		WHERE id = ? AND status IN (?, ?)`,
		string(StatusPending), reason, id,
		string(StatusRejected), string(StatusRevoked))
	if err != nil {
		return false, fmt.Errorf("friendship: re-pending: %w", err)
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// UpdateStatus 把 fromStatus → toStatus。带 fromStatus 校验避免并发踩。
func (r *SQLRepo) UpdateStatus(ctx context.Context, id int64, fromStatus, toStatus Status) (bool, error) {
	res, err := r.db.ExecContext(ctx, `
		UPDATE friendships SET status = ? WHERE id = ? AND status = ?`,
		string(toStatus), id, string(fromStatus))
	if err != nil {
		return false, fmt.Errorf("friendship: transition: %w", err)
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

func (r *SQLRepo) ListInvolvingAgent(ctx context.Context, agentID string) ([]*Friendship, error) {
	rows, err := r.db.QueryContext(ctx,
		selectCols+`WHERE from_agent_id = ? OR to_agent_id = ?
		ORDER BY updated_at DESC LIMIT 500`, agentID, agentID)
	if err != nil {
		return nil, fmt.Errorf("friendship: list: %w", err)
	}
	defer rows.Close()
	return scanMany(rows)
}

func (r *SQLRepo) ListIncomingPending(ctx context.Context, agentID string) ([]*Friendship, error) {
	rows, err := r.db.QueryContext(ctx,
		selectCols+`WHERE to_agent_id = ? AND status = ?
		ORDER BY created_at DESC LIMIT 500`,
		agentID, string(StatusPending))
	if err != nil {
		return nil, fmt.Errorf("friendship: list incoming: %w", err)
	}
	defer rows.Close()
	return scanMany(rows)
}

// ExistsAccepted 双向查询：只要任一方向存在 accepted 行就返回 true。
// 刻意用 LIMIT 1 的 SELECT 而不是 COUNT，走 idx_status 索引更快。
func (r *SQLRepo) ExistsAccepted(ctx context.Context, a, b string) (bool, error) {
	var n int
	err := r.db.QueryRowContext(ctx, `
		SELECT 1 FROM friendships
		WHERE status = ?
		  AND ((from_agent_id = ? AND to_agent_id = ?)
		    OR (from_agent_id = ? AND to_agent_id = ?))
		LIMIT 1`,
		string(StatusAccepted), a, b, b, a).Scan(&n)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, fmt.Errorf("friendship: exists accepted: %w", err)
	}
	return true, nil
}

// ── scan helpers ────────────────────────────────────────────────────────

type scanner interface {
	Scan(dest ...any) error
}

func scanOne(s scanner) (*Friendship, error) {
	var (
		f   Friendship
		st  string
		cr  time.Time
		upd time.Time
	)
	err := s.Scan(&f.ID, &f.FromAgentID, &f.ToAgentID, &st, &f.Reason, &cr, &upd)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	f.Status = Status(st)
	f.CreatedAt = cr
	f.UpdatedAt = upd
	return &f, nil
}

func scanMany(rows *sql.Rows) ([]*Friendship, error) {
	out := make([]*Friendship, 0, 8)
	for rows.Next() {
		f, err := scanOne(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}
