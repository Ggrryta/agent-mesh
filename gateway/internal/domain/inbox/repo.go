package inbox

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// SQLRepo 是 inbox 的 MySQL 实现。
type SQLRepo struct{ db *sql.DB }

func NewSQLRepo(db *sql.DB) *SQLRepo { return &SQLRepo{db: db} }

// Insert 写入一行 inbox_events。id 由 DB 自增。
func (r *SQLRepo) Insert(ctx context.Context, e *Event) (*Event, error) {
	if e.AgentID == "" {
		return nil, ErrEmptyAgent
	}
	res, err := r.db.ExecContext(ctx, `
		INSERT INTO inbox_events
			(agent_id, kind, task_id, ref_id, payload_json)
		VALUES (?, ?, ?, ?, ?)`,
		e.AgentID, string(e.Kind), e.TaskID, e.RefID, []byte(e.Payload),
	)
	if err != nil {
		return nil, fmt.Errorf("inbox: insert: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("inbox: last id: %w", err)
	}
	row := r.db.QueryRowContext(ctx, `
		SELECT id, agent_id, kind, task_id, ref_id, payload_json, created_at, delivered_at
		FROM inbox_events WHERE id = ? LIMIT 1`, id)
	return scanEvent(row)
}

// ListSince 按 agent 拉事件，id > sinceID，升序，limit 截断。
// limit <= 0 走默认 100；上限 500（防客户端一次拉爆）。
func (r *SQLRepo) ListSince(ctx context.Context, agentID string, sinceID int64, limit int) ([]*Event, error) {
	if agentID == "" {
		return nil, ErrEmptyAgent
	}
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, agent_id, kind, task_id, ref_id, payload_json, created_at, delivered_at
		FROM inbox_events
		WHERE agent_id = ? AND id > ?
		ORDER BY id ASC
		LIMIT ?`, agentID, sinceID, limit)
	if err != nil {
		return nil, fmt.Errorf("inbox: list since: %w", err)
	}
	defer rows.Close()
	out := make([]*Event, 0, 16)
	for rows.Next() {
		e, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// MarkDelivered 批量打标。push worker 成功时调。ids 空时 no-op。
func (r *SQLRepo) MarkDelivered(ctx context.Context, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	q := fmt.Sprintf(`UPDATE inbox_events SET delivered_at = NOW(3) WHERE id IN (%s)`,
		strings.Join(placeholders, ","))
	_, err := r.db.ExecContext(ctx, q, args...)
	if err != nil {
		return fmt.Errorf("inbox: mark delivered: %w", err)
	}
	return nil
}

// ── scan helper ───────────────────────────────────────────────────

type scanner interface{ Scan(dest ...any) error }

func scanEvent(s scanner) (*Event, error) {
	var (
		e       Event
		kind    string
		payload []byte
		dn      sql.NullTime
	)
	err := s.Scan(&e.ID, &e.AgentID, &kind, &e.TaskID, &e.RefID, &payload, &e.CreatedAt, &dn)
	if err != nil {
		return nil, err
	}
	e.Kind = Kind(kind)
	e.Payload = payload
	if dn.Valid {
		t := dn.Time
		e.DeliveredAt = &t
	}
	return &e, nil
}

// now 可由测试覆盖；MVP 用 time.Now
var now = func() time.Time { return time.Now() }
