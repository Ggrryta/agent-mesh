package publication

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// Repo 数据访问接口。
type Repo interface {
	Insert(ctx context.Context, p *Publication) (*Publication, error)
	GetByID(ctx context.Context, id int64) (*Publication, error)
	List(ctx context.Context, f Filter) ([]*Publication, error)
	IncrementDownload(ctx context.Context, id int64) error
	DeleteOwned(ctx context.Context, id, publisherUID int64) (bool, error)

	InsertSubscription(ctx context.Context, sub *Subscription) (*Subscription, error)
	ListSubscriptionsByUser(ctx context.Context, uid int64) ([]*Subscription, error)
	HasSubscription(ctx context.Context, uid, publicationID int64) (bool, error)
}

type SQLRepo struct{ db *sql.DB }

func NewSQLRepo(db *sql.DB) *SQLRepo { return &SQLRepo{db: db} }

func (r *SQLRepo) Insert(ctx context.Context, p *Publication) (*Publication, error) {
	res, err := r.db.ExecContext(ctx, `
		INSERT INTO agent_publications (publisher_uid, source_agent_id, title, summary, system_prompt_template, category, tags)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		p.PublisherUID, p.SourceAgentID, p.Title, p.Summary, nullableStr(p.SystemPromptTemplate),
		p.Category, SerializeTags(p.Tags))
	if err != nil {
		return nil, fmt.Errorf("publication: insert: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("publication: last insert id: %w", err)
	}
	return r.GetByID(ctx, id)
}

func (r *SQLRepo) GetByID(ctx context.Context, id int64) (*Publication, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT id, publisher_uid, source_agent_id, title, summary, system_prompt_template, category, tags, download_count, created_at, updated_at
		FROM agent_publications
		WHERE id = ?`, id)
	p, err := scanPub(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrPublicationNotFound
		}
		return nil, err
	}
	return p, nil
}

func (r *SQLRepo) List(ctx context.Context, f Filter) ([]*Publication, error) {
	limit := f.Limit
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	offset := f.Offset
	if offset < 0 {
		offset = 0
	}

	var (
		conds []string
		args  []any
	)
	if f.Category != "" {
		conds = append(conds, "category = ?")
		args = append(args, f.Category)
	}
	if f.PublisherUID != 0 {
		conds = append(conds, "publisher_uid = ?")
		args = append(args, f.PublisherUID)
	}
	if f.Search != "" {
		// 模糊搜索 title / summary / tags；用 LIKE 而不是 fulltext —— MVP 数据量不大。
		like := "%" + f.Search + "%"
		conds = append(conds, "(title LIKE ? OR summary LIKE ? OR tags LIKE ?)")
		args = append(args, like, like, like)
	}
	where := ""
	if len(conds) > 0 {
		where = "WHERE " + strings.Join(conds, " AND ")
	}
	args = append(args, limit, offset)
	q := fmt.Sprintf(`
		SELECT id, publisher_uid, source_agent_id, title, summary, system_prompt_template, category, tags, download_count, created_at, updated_at
		FROM agent_publications
		%s
		ORDER BY created_at DESC, id DESC
		LIMIT ? OFFSET ?`, where)

	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("publication: list: %w", err)
	}
	defer rows.Close()
	out := make([]*Publication, 0, limit)
	for rows.Next() {
		p, err := scanPub(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (r *SQLRepo) IncrementDownload(ctx context.Context, id int64) error {
	_, err := r.db.ExecContext(ctx,
		"UPDATE agent_publications SET download_count = download_count + 1 WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("publication: increment download: %w", err)
	}
	return nil
}

func (r *SQLRepo) DeleteOwned(ctx context.Context, id, publisherUID int64) (bool, error) {
	res, err := r.db.ExecContext(ctx,
		"DELETE FROM agent_publications WHERE id = ? AND publisher_uid = ?",
		id, publisherUID)
	if err != nil {
		return false, fmt.Errorf("publication: delete: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("publication: rows affected: %w", err)
	}
	return n > 0, nil
}

func (r *SQLRepo) InsertSubscription(ctx context.Context, sub *Subscription) (*Subscription, error) {
	res, err := r.db.ExecContext(ctx, `
		INSERT INTO agent_subscriptions (uid, publication_id, forked_agent_id)
		VALUES (?, ?, ?)`,
		sub.UID, sub.PublicationID, sub.ForkedAgentID)
	if err != nil {
		// 唯一键冲突 → ErrAlreadySubscribed（service 层判断）
		if isDuplicateEntry(err) {
			return nil, ErrAlreadySubscribed
		}
		return nil, fmt.Errorf("publication: insert subscription: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("publication: subscription last insert id: %w", err)
	}
	row := r.db.QueryRowContext(ctx, `
		SELECT id, uid, publication_id, forked_agent_id, created_at
		FROM agent_subscriptions WHERE id = ?`, id)
	out := &Subscription{}
	if err := row.Scan(&out.ID, &out.UID, &out.PublicationID, &out.ForkedAgentID, &out.CreatedAt); err != nil {
		return nil, fmt.Errorf("publication: scan subscription: %w", err)
	}
	return out, nil
}

func (r *SQLRepo) ListSubscriptionsByUser(ctx context.Context, uid int64) ([]*Subscription, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, uid, publication_id, forked_agent_id, created_at
		FROM agent_subscriptions
		WHERE uid = ?
		ORDER BY id DESC`, uid)
	if err != nil {
		return nil, fmt.Errorf("publication: list subscriptions: %w", err)
	}
	defer rows.Close()
	out := make([]*Subscription, 0)
	for rows.Next() {
		s := &Subscription{}
		if err := rows.Scan(&s.ID, &s.UID, &s.PublicationID, &s.ForkedAgentID, &s.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (r *SQLRepo) HasSubscription(ctx context.Context, uid, publicationID int64) (bool, error) {
	var n int
	err := r.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM agent_subscriptions WHERE uid = ? AND publication_id = ?",
		uid, publicationID).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("publication: has subscription: %w", err)
	}
	return n > 0, nil
}

// ─── helpers ────────────────────────────────────────────────────────────

func scanPub(s scanner) (*Publication, error) {
	var (
		p          Publication
		promptNS   sql.NullString
		tagsString string
	)
	if err := s.Scan(
		&p.ID, &p.PublisherUID, &p.SourceAgentID, &p.Title, &p.Summary,
		&promptNS, &p.Category, &tagsString, &p.DownloadCount,
		&p.CreatedAt, &p.UpdatedAt,
	); err != nil {
		return nil, err
	}
	if promptNS.Valid {
		p.SystemPromptTemplate = promptNS.String
	}
	p.Tags = ParseTags(tagsString)
	return &p, nil
}

type scanner interface {
	Scan(dest ...any) error
}

func nullableStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func isDuplicateEntry(err error) bool {
	if err == nil {
		return false
	}
	// MySQL 1062
	return strings.Contains(err.Error(), "Error 1062") ||
		strings.Contains(err.Error(), "Duplicate entry")
}
