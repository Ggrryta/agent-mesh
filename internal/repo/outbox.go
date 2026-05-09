package repo

import (
	"context"
	"time"

	"agent-gateway/internal/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type OutboxRepo struct {
	db *gorm.DB
}

func NewOutboxRepo(db *gorm.DB) *OutboxRepo {
	return &OutboxRepo{db: db}
}

func (r *OutboxRepo) ListPending(ctx context.Context, limit int) ([]*model.OutboxEvent, error) {
	if limit <= 0 {
		limit = 100
	}
	now := time.Now()
	var events []*model.OutboxEvent
	err := r.db.WithContext(ctx).
		Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).
		Where("status = ? AND (next_retry_at IS NULL OR next_retry_at <= ?)", model.OutboxStatusPending, now).
		Order("id ASC").
		Limit(limit).
		Find(&events).Error
	return events, err
}

func (r *OutboxRepo) MarkSent(ctx context.Context, eventID string) error {
	now := time.Now()
	return r.db.WithContext(ctx).Model(&model.OutboxEvent{}).
		Where("event_id = ?", eventID).
		Updates(map[string]any{
			"status":     model.OutboxStatusSent,
			"sent_at":    now,
			"updated_at": now,
		}).Error
}

func (r *OutboxRepo) MarkRetry(ctx context.Context, eventID string, delay time.Duration) error {
	nextRetryAt := time.Now().Add(delay)
	return r.db.WithContext(ctx).Model(&model.OutboxEvent{}).
		Where("event_id = ?", eventID).
		Updates(map[string]any{
			"retries":       gorm.Expr("retries + 1"),
			"next_retry_at": nextRetryAt,
			"updated_at":    time.Now(),
		}).Error
}

func (r *OutboxRepo) MarkFailed(ctx context.Context, eventID string) error {
	return r.db.WithContext(ctx).Model(&model.OutboxEvent{}).
		Where("event_id = ?", eventID).
		Updates(map[string]any{
			"status":     model.OutboxStatusFailed,
			"updated_at": time.Now(),
		}).Error
}
