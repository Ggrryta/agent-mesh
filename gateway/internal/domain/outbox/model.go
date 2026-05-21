package outbox

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

var ErrEventNotFound = errors.New("outbox: event not found")

type Status string

const (
	StatusPending Status = "pending"
	StatusSent    Status = "sent"
	StatusFailed  Status = "failed"
)

type Event struct {
	ID          int64
	EventType   string
	Payload     json.RawMessage
	Status      Status
	Retries     int
	NextRetryAt *time.Time
	CreatedAt   time.Time
	SentAt      *time.Time
}

// Repo outbox 数据访问接口。
type Repo interface {
	Insert(ctx context.Context, eventType string, payload json.RawMessage) (*Event, error)
	ClaimBatch(ctx context.Context, limit int) ([]*Event, error)
	MarkSent(ctx context.Context, id int64) error
	MarkFailed(ctx context.Context, id int64) error
	IncrRetry(ctx context.Context, id int64, nextRetry time.Time) error
}
