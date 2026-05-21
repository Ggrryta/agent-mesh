// Package kafka 提供 Kafka producer 封装。
// Phase 1：双写模式——inbox 表仍是主路径，Kafka 是异步副本。
package kafka

import (
	"context"
	"strings"
	"time"

	"github.com/segmentio/kafka-go"
	"go.uber.org/zap"
)

// Producer 是 Kafka 消息生产者。线程安全，可被多个 goroutine 共享。
type Producer struct {
	writer *kafka.Writer
	log    *zap.Logger
}

// NewProducer 创建 Kafka producer。brokers 是逗号分隔的地址列表。
func NewProducer(brokers string, log *zap.Logger) *Producer {
	addrs := strings.Split(brokers, ",")
	w := &kafka.Writer{
		Addr:         kafka.TCP(addrs...),
		Balancer:     &kafka.Hash{}, // 按 key hash 分 partition，保证同 agent 有序
		BatchTimeout: 10 * time.Millisecond,
		BatchSize:    100,
		Async:        true, // 异步写入，不阻塞调用方
		Logger:       kafka.LoggerFunc(func(msg string, args ...interface{}) { log.Debug(msg, zap.Any("args", args)) }),
		ErrorLogger:  kafka.LoggerFunc(func(msg string, args ...interface{}) { log.Warn(msg, zap.Any("args", args)) }),
	}
	return &Producer{writer: w, log: log}
}

// Publish 发送一条消息到指定 topic。key 用于 partition 路由。
// 异步模式下不会阻塞，错误通过 ErrorLogger 记录。
func (p *Producer) Publish(ctx context.Context, topic, key string, value []byte) error {
	return p.writer.WriteMessages(ctx, kafka.Message{
		Topic: topic,
		Key:   []byte(key),
		Value: value,
	})
}

// Close 关闭 producer，flush 缓冲区。
func (p *Producer) Close() error {
	return p.writer.Close()
}

// Ping 检查 Kafka 连通性（用于 readiness probe）。
func Ping(ctx context.Context, brokers string) error {
	addrs := strings.Split(brokers, ",")
	conn, err := kafka.DialContext(ctx, "tcp", addrs[0])
	if err != nil {
		return err
	}
	defer conn.Close()
	_, err = conn.Brokers()
	return err
}
