package service

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

var ErrTaskQueueFull = errors.New("task queue full")

type TaskEvent struct {
	EventID   string    `json:"event_id"`
	EventType string    `json:"event_type"`
	TaskID    string    `json:"task_id"`
	AgentID   string    `json:"agent_id,omitempty"`
	SkillID   string    `json:"skill_id,omitempty"`
	AppID     string    `json:"app_id,omitempty"`
	CreatedAt time.Time `json:"created_at,omitempty"`
}

type TaskEventPublisher interface {
	Publish(ctx context.Context, event TaskEvent) error
}

type InMemoryTaskQueue struct {
	ch chan TaskEvent
}

func NewInMemoryTaskQueue(buffer int) *InMemoryTaskQueue {
	if buffer <= 0 {
		buffer = 1024
	}
	return &InMemoryTaskQueue{ch: make(chan TaskEvent, buffer)}
}

func (q *InMemoryTaskQueue) Publish(ctx context.Context, event TaskEvent) error {
	select {
	case q.ch <- event:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	default:
		return ErrTaskQueueFull
	}
}

func (q *InMemoryTaskQueue) Subscribe() <-chan TaskEvent {
	return q.ch
}

func DecodeTaskEvent(payload []byte) (TaskEvent, error) {
	var event TaskEvent
	err := json.Unmarshal(payload, &event)
	return event, err
}
