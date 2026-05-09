package service

import (
	"context"
	"sync"
	"time"

	"agent-gateway/internal/model"
	"agent-gateway/internal/repo"
	"agent-gateway/pkg/logger"

	"go.uber.org/zap"
)

type OutboxDispatcher struct {
	outboxRepo *repo.OutboxRepo
	publisher  TaskEventPublisher
	interval   time.Duration
	batchSize  int
	maxRetries int
	done       chan struct{}
	stopOnce   sync.Once
}

func NewOutboxDispatcher(outboxRepo *repo.OutboxRepo, publisher TaskEventPublisher) *OutboxDispatcher {
	return &OutboxDispatcher{
		outboxRepo: outboxRepo,
		publisher:  publisher,
		interval:   time.Second,
		batchSize:  100,
		maxRetries: 10,
		done:       make(chan struct{}),
	}
}

func (d *OutboxDispatcher) Start() {
	go func() {
		ticker := time.NewTicker(d.interval)
		defer ticker.Stop()
		for {
			select {
			case <-d.done:
				return
			case <-ticker.C:
				d.dispatchOnce()
			}
		}
	}()
	logger.Info("outbox dispatcher started")
}

func (d *OutboxDispatcher) Stop() {
	d.stopOnce.Do(func() {
		close(d.done)
	})
}

func (d *OutboxDispatcher) dispatchOnce() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	events, err := d.outboxRepo.ListPending(ctx, d.batchSize)
	if err != nil {
		logger.Error("outbox dispatcher: list pending events failed", zap.Error(err))
		return
	}
	for _, event := range events {
		d.dispatchEvent(ctx, event)
	}
}

func (d *OutboxDispatcher) dispatchEvent(ctx context.Context, event *model.OutboxEvent) {
	taskEvent, err := DecodeTaskEvent(event.Payload)
	if err != nil {
		logger.Error("outbox dispatcher: decode event failed",
			zap.String("event_id", event.EventID),
			zap.Error(err))
		_ = d.outboxRepo.MarkFailed(ctx, event.EventID)
		return
	}
	if taskEvent.EventID == "" {
		taskEvent.EventID = event.EventID
	}
	if taskEvent.EventType == "" {
		taskEvent.EventType = event.EventType
	}
	if taskEvent.TaskID == "" {
		taskEvent.TaskID = event.AggregateID
	}

	if err := d.publisher.Publish(ctx, taskEvent); err != nil {
		logger.Error("outbox dispatcher: publish failed",
			zap.String("event_id", event.EventID),
			zap.String("task_id", taskEvent.TaskID),
			zap.Error(err))
		if event.Retries+1 >= d.maxRetries {
			_ = d.outboxRepo.MarkFailed(ctx, event.EventID)
			return
		}
		delay := time.Duration(event.Retries+1) * 5 * time.Second
		_ = d.outboxRepo.MarkRetry(ctx, event.EventID, delay)
		return
	}

	if err := d.outboxRepo.MarkSent(ctx, event.EventID); err != nil {
		logger.Error("outbox dispatcher: mark sent failed",
			zap.String("event_id", event.EventID),
			zap.Error(err))
	}
}
