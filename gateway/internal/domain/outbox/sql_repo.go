package outbox

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// SQLRepo 是 MySQL 实现。
type SQLRepo struct{ db *sql.DB }

func NewSQLRepo(db *sql.DB) *SQLRepo { return &SQLRepo{db: db} }

func (r *SQLRepo) Insert(ctx context.Context, eventType string, payload json.RawMessage) (*Event, error) {
	return r.insertWith(ctx, r.db, eventType, payload)
}

// InsertTx 在已有事务内写 outbox（Transactional Outbox 模式的核心）。
func (r *SQLRepo) InsertTx(ctx context.Context, tx *sql.Tx, eventType string, payload json.RawMessage) (*Event, error) {
	return r.insertWith(ctx, tx, eventType, payload)
}

type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func (r *SQLRepo) insertWith(ctx context.Context, db execer, eventType string, payload json.RawMessage) (*Event, error) {
	eventID := fmt.Sprintf("evt-%d", time.Now().UnixNano())
	res, err := db.ExecContext(ctx,
		"INSERT INTO outbox_events (event_id, aggregate_type, aggregate_id, event_type, payload) VALUES (?, ?, ?, ?, ?)",
		eventID, "inbox", eventType, eventType, payload)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return &Event{
		ID: id, EventType: eventType, Payload: payload,
		Status: StatusPending, CreatedAt: time.Now(),
	}, nil
}

func (r *SQLRepo) ClaimBatch(ctx context.Context, limit int) ([]*Event, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, event_type, payload, status, retries, next_run_at, created_at
		FROM outbox_events
		WHERE status = 'pending'
		  AND (next_run_at IS NULL OR next_run_at <= NOW())
		ORDER BY id
		LIMIT ?
		FOR UPDATE SKIP LOCKED`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*Event
	for rows.Next() {
		var e Event
		var nextRetry sql.NullTime
		if err := rows.Scan(&e.ID, &e.EventType, &e.Payload, &e.Status, &e.Retries, &nextRetry, &e.CreatedAt); err != nil {
			return nil, err
		}
		if nextRetry.Valid {
			e.NextRetryAt = &nextRetry.Time
		}
		out = append(out, &e)
	}
	return out, rows.Err()
}

func (r *SQLRepo) MarkSent(ctx context.Context, id int64) error {
	_, err := r.db.ExecContext(ctx,
		"UPDATE outbox_events SET status = 'sent', updated_at = NOW() WHERE id = ?", id)
	return err
}

func (r *SQLRepo) MarkFailed(ctx context.Context, id int64) error {
	_, err := r.db.ExecContext(ctx,
		"UPDATE outbox_events SET status = 'failed', updated_at = NOW() WHERE id = ?", id)
	return err
}

func (r *SQLRepo) IncrRetry(ctx context.Context, id int64, nextRetry time.Time) error {
	_, err := r.db.ExecContext(ctx,
		"UPDATE outbox_events SET retries = retries + 1, next_run_at = ?, updated_at = NOW() WHERE id = ?",
		nextRetry, id)
	return err
}

// AsTaskOutboxWriter 返回一个适配器，满足 task.OutboxWriter 接口。
type TaskOutboxAdapter struct{ repo *SQLRepo }

func (r *SQLRepo) AsTaskOutboxWriter() *TaskOutboxAdapter {
	return &TaskOutboxAdapter{repo: r}
}

func (a *TaskOutboxAdapter) Insert(ctx context.Context, eventType string, payload []byte) error {
	_, err := a.repo.Insert(ctx, eventType, json.RawMessage(payload))
	return err
}

func (a *TaskOutboxAdapter) MarkSentByEventType(ctx context.Context, eventType string, payload []byte) error {
	// 标记最近一条匹配的 pending 事件为 sent（乐观直发成功后调用）
	_, err := a.repo.db.ExecContext(ctx, `
		UPDATE outbox_events SET status = 'sent', updated_at = NOW()
		WHERE event_type = ? AND status = 'pending'
		ORDER BY id DESC LIMIT 1`, eventType)
	return err
}

// ── Dead Letter ──

// DeadLetter 是死信表的一行。
type DeadLetter struct {
	ID                int64
	OriginalID        int64
	EventID           string
	EventType         string
	Payload           json.RawMessage
	ErrorMsg          string
	Retries           int
	OriginalCreatedAt time.Time
	DeadAt            time.Time
}

// MoveToDeadLetter 把一个 failed 事件移入死信表。
func (r *SQLRepo) MoveToDeadLetter(ctx context.Context, e *Event, errMsg string) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO outbox_dead_letters (original_id, event_id, event_type, payload, error_msg, retries, original_created_at)
		SELECT id, event_id, event_type, payload, ?, retries, created_at
		FROM outbox_events WHERE id = ?`,
		errMsg, e.ID)
	if err != nil {
		return fmt.Errorf("outbox: move to dead letter: %w", err)
	}
	// 从 outbox 表删除（不再重试）
	_, err = r.db.ExecContext(ctx, "DELETE FROM outbox_events WHERE id = ?", e.ID)
	return err
}

// ListDeadLetters 列出死信（未处理的），按时间倒序。
func (r *SQLRepo) ListDeadLetters(ctx context.Context, limit int) ([]*DeadLetter, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, original_id, event_id, event_type, payload, error_msg, retries, original_created_at, dead_at
		FROM outbox_dead_letters
		WHERE resolved_at IS NULL
		ORDER BY dead_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*DeadLetter
	for rows.Next() {
		var dl DeadLetter
		if err := rows.Scan(&dl.ID, &dl.OriginalID, &dl.EventID, &dl.EventType, &dl.Payload, &dl.ErrorMsg, &dl.Retries, &dl.OriginalCreatedAt, &dl.DeadAt); err != nil {
			return nil, err
		}
		out = append(out, &dl)
	}
	return out, rows.Err()
}

// RetryDeadLetter 把死信重新放回 outbox 表重试。
func (r *SQLRepo) RetryDeadLetter(ctx context.Context, id int64) error {
	// 从死信表读出事件
	var eventType string
	var payload json.RawMessage
	err := r.db.QueryRowContext(ctx,
		"SELECT event_type, payload FROM outbox_dead_letters WHERE id = ? AND resolved_at IS NULL", id).
		Scan(&eventType, &payload)
	if err != nil {
		return fmt.Errorf("outbox: dead letter not found: %w", err)
	}
	// 重新插入 outbox
	if _, err := r.Insert(ctx, eventType, payload); err != nil {
		return err
	}
	// 标记死信为已处理
	_, err = r.db.ExecContext(ctx,
		"UPDATE outbox_dead_letters SET resolved_at = NOW(), resolved_by = 'retry' WHERE id = ?", id)
	return err
}

// ── Pod 注册 ──

// PodHeartbeat 注册/续约 Pod。
func (r *SQLRepo) PodHeartbeat(ctx context.Context, podID string) {
	r.db.ExecContext(ctx, `
		INSERT INTO dispatcher_pods (pod_id, heartbeat_at) VALUES (?, NOW())
		ON DUPLICATE KEY UPDATE heartbeat_at = NOW()`, podID)
}

// ListActivePods 返回活跃 Pod 列表（按 pod_id 排序，用于确定 index）。
func (r *SQLRepo) ListActivePods(ctx context.Context, expiry time.Duration) ([]string, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT pod_id FROM dispatcher_pods
		WHERE heartbeat_at > NOW() - INTERVAL ? SECOND
		ORDER BY pod_id ASC`, int(expiry.Seconds()))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var pods []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		pods = append(pods, id)
	}
	return pods, rows.Err()
}

// ── Hash 分片查询 ──

// ClaimByHashSlot 查询属于指定 hash 槽位的 pending 事件。
// 两层 hash：第一层 % totalPods 确定 Pod，第二层 DIV totalPods % workers 确定 worker。
func (r *SQLRepo) ClaimByHashSlot(ctx context.Context, totalPods, podIndex, workers, workerIndex, limit int) ([]*Event, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, event_type, payload, status, retries, next_run_at, created_at
		FROM outbox_events
		WHERE status = 'pending'
		  AND (next_run_at IS NULL OR next_run_at <= NOW())
		  AND MOD(CRC32(target_agent_id), ?) = ?
		  AND MOD(FLOOR(CRC32(target_agent_id) / ?), ?) = ?
		ORDER BY id ASC
		LIMIT ?`,
		totalPods, podIndex,
		totalPods, workers, workerIndex,
		limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Event
	for rows.Next() {
		var e Event
		var nextRetry sql.NullTime
		if err := rows.Scan(&e.ID, &e.EventType, &e.Payload, &e.Status, &e.Retries, &nextRetry, &e.CreatedAt); err != nil {
			return nil, err
		}
		if nextRetry.Valid {
			e.NextRetryAt = &nextRetry.Time
		}
		out = append(out, &e)
	}
	return out, rows.Err()
}
