package service

import (
	"context"
	"encoding/json"
	"time"

	"agent-gateway/pkg/logger"

	"github.com/segmentio/kafka-go"
	"go.uber.org/zap"
)

type KafkaTaskQueueConfig struct {
	Brokers []string
	Topic   string
	GroupID string
	Buffer  int
}

type KafkaTaskQueue struct {
	writer *kafka.Writer
	reader *kafka.Reader
	ch     chan TaskEvent
	done   chan struct{}
}

func NewKafkaTaskQueue(cfg KafkaTaskQueueConfig) *KafkaTaskQueue {
	if cfg.Buffer <= 0 {
		cfg.Buffer = 4096
	}
	writer := &kafka.Writer{
		Addr:         kafka.TCP(cfg.Brokers...),
		Topic:        cfg.Topic,
		Balancer:     &kafka.Hash{},
		RequiredAcks: kafka.RequireAll,
		Async:        false,
		BatchTimeout: 10 * time.Millisecond,
	}
	reader := kafka.NewReader(kafka.ReaderConfig{
		Brokers:        cfg.Brokers,
		Topic:          cfg.Topic,
		GroupID:        cfg.GroupID,
		MinBytes:       1,
		MaxBytes:       10e6,
		CommitInterval: time.Second,
	})
	return &KafkaTaskQueue{
		writer: writer,
		reader: reader,
		ch:     make(chan TaskEvent, cfg.Buffer),
		done:   make(chan struct{}),
	}
}

func (q *KafkaTaskQueue) Publish(ctx context.Context, event TaskEvent) error {
	payload, err := json.Marshal(event)
	if err != nil {
		return err
	}
	return q.writer.WriteMessages(ctx, kafka.Message{
		Key:   []byte(event.TaskID),
		Value: payload,
		Time:  time.Now(),
	})
}

func (q *KafkaTaskQueue) Subscribe() <-chan TaskEvent {
	return q.ch
}

func (q *KafkaTaskQueue) Start() {
	go func() {
		for {
			msg, err := q.reader.ReadMessage(context.Background())
			if err != nil {
				select {
				case <-q.done:
					return
				default:
				}
				logger.Error("kafka task queue: read message failed", zap.Error(err))
				time.Sleep(time.Second)
				continue
			}

			var event TaskEvent
			if err := json.Unmarshal(msg.Value, &event); err != nil {
				logger.Error("kafka task queue: decode message failed", zap.Error(err))
				continue
			}
			if event.TaskID == "" {
				logger.Warn("kafka task queue: skip message without task_id")
				continue
			}

			select {
			case q.ch <- event:
			case <-q.done:
				return
			}
		}
	}()
	logger.Info("kafka task queue started")
}

func (q *KafkaTaskQueue) Close() error {
	close(q.done)
	if err := q.reader.Close(); err != nil {
		_ = q.writer.Close()
		return err
	}
	return q.writer.Close()
}
