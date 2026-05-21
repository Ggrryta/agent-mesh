package apikey

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Repo 是数据访问接口。service 依赖它，不直接依赖 *sql.DB。
type Repo interface {
	Insert(ctx context.Context, ownerUID int64, keyHash, prefix, label string) (*Key, error)
	FindByHash(ctx context.Context, keyHash string) (*Key, error)
	ListByOwner(ctx context.Context, ownerUID int64) ([]*Key, error)
	Revoke(ctx context.Context, ownerUID, id int64) error
	TouchLastUsed(ctx context.Context, id int64, ts time.Time) error
}

// SQLRepo 是 MySQL 实现。
type SQLRepo struct {
	db *sql.DB
}

func NewSQLRepo(db *sql.DB) *SQLRepo { return &SQLRepo{db: db} }

// Insert 写入一行 api_keys。调用方必须先算好 key_hash，本层只负责持久化。
func (r *SQLRepo) Insert(ctx context.Context, ownerUID int64, keyHash, prefix, label string) (*Key, error) {
	res, err := r.db.ExecContext(ctx, `
		INSERT INTO api_keys (owner_uid, key_hash, key_prefix, label)
		VALUES (?, ?, ?, ?)`,
		ownerUID, keyHash, prefix, label)
	if err != nil {
		return nil, fmt.Errorf("apikey: insert: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("apikey: last insert id: %w", err)
	}
	return r.getByID(ctx, id)
}

// FindByHash 走 uk_key_hash 唯一索引；找不到返回 ErrKeyNotFound。
// 不在这里判 revoked_at —— service 层根据业务语义决定如何处理吊销态。
func (r *SQLRepo) FindByHash(ctx context.Context, keyHash string) (*Key, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT id, owner_uid, key_prefix, label, last_used_at, revoked_at, created_at
		FROM api_keys
		WHERE key_hash = ?
		LIMIT 1`, keyHash)
	return scanKey(row)
}

// ListByOwner 按创建时间倒序返回该用户全部 key（含已吊销，UI 可筛）。
// MVP 不分页：单用户数十把 key 以内足够。
func (r *SQLRepo) ListByOwner(ctx context.Context, ownerUID int64) ([]*Key, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, owner_uid, key_prefix, label, last_used_at, revoked_at, created_at
		FROM api_keys
		WHERE owner_uid = ?
		ORDER BY id DESC
		LIMIT 200`, ownerUID)
	if err != nil {
		return nil, fmt.Errorf("apikey: list: %w", err)
	}
	defer rows.Close()
	out := make([]*Key, 0, 8)
	for rows.Next() {
		k, err := scanKey(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// Revoke 把某个 key 的 revoked_at 置为 NOW()。对同 owner 幂等：
//   - 第一次：RowsAffected = 1
//   - 第二次（已吊销）：RowsAffected = 0，视为成功（幂等）
//
// 不是本 owner 的 key 也返回 0；service 层根据该值决定要不要回 404。
func (r *SQLRepo) Revoke(ctx context.Context, ownerUID, id int64) error {
	res, err := r.db.ExecContext(ctx, `
		UPDATE api_keys SET revoked_at = NOW(3)
		WHERE id = ? AND owner_uid = ? AND revoked_at IS NULL`, id, ownerUID)
	if err != nil {
		return fmt.Errorf("apikey: revoke: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		// 要么不存在，要么不是 owner，要么已经是 revoked。
		// 统一看成 "no-op" —— 对调用方而言 "吊销成功" 的语义不变。
		// 真正想区分 "找不到" 的场景由 service 层调 getByID 再判。
		if exists, _ := r.existsForOwner(ctx, ownerUID, id); !exists {
			return ErrKeyNotFound
		}
	}
	return nil
}

// TouchLastUsed 只更新 last_used_at。用于 Verify 成功后的异步统计。
// 失败不影响主流程，调用方应该 fire-and-forget。
func (r *SQLRepo) TouchLastUsed(ctx context.Context, id int64, ts time.Time) error {
	_, err := r.db.ExecContext(ctx,
		"UPDATE api_keys SET last_used_at = ? WHERE id = ?", ts, id)
	return err
}

func (r *SQLRepo) getByID(ctx context.Context, id int64) (*Key, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT id, owner_uid, key_prefix, label, last_used_at, revoked_at, created_at
		FROM api_keys WHERE id = ? LIMIT 1`, id)
	return scanKey(row)
}

func (r *SQLRepo) existsForOwner(ctx context.Context, ownerUID, id int64) (bool, error) {
	var n int
	err := r.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM api_keys WHERE id = ? AND owner_uid = ?", id, ownerUID).Scan(&n)
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// scanner 统一 *sql.Row 和 *sql.Rows 的 Scan 行为。
type scanner interface {
	Scan(dest ...any) error
}

func scanKey(s scanner) (*Key, error) {
	var (
		k         Key
		lastUsed  sql.NullTime
		revokedAt sql.NullTime
	)
	err := s.Scan(&k.ID, &k.OwnerUID, &k.KeyPrefix, &k.Label, &lastUsed, &revokedAt, &k.CreatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrKeyNotFound
		}
		return nil, err
	}
	if lastUsed.Valid {
		t := lastUsed.Time
		k.LastUsedAt = &t
	}
	if revokedAt.Valid {
		t := revokedAt.Time
		k.RevokedAt = &t
	}
	return &k, nil
}
