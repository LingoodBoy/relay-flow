package queue

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	amqp "github.com/rabbitmq/amqp091-go"

	"relay-flow/internal/event"
)

type RunEventStore interface {
	AppendRunEvent(ctx context.Context, evt event.RunEvent) error
}

// EventConsumer 消费 RabbitMQ Run 事件，并把事件持久化到 Redis。
type EventConsumer struct {
	conn  *amqp.Connection
	ch    *amqp.Channel
	store RunEventStore
}

// NewEventConsumer 创建 Gateway 事件消费者。
func NewEventConsumer(rabbitMQURL string, store RunEventStore) (*EventConsumer, error) {
	conn, err := amqp.Dial(rabbitMQURL)
	if err != nil {
		return nil, fmt.Errorf("dial rabbitmq: %w", err)
	}

	ch, err := conn.Channel()
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("open rabbitmq channel: %w", err)
	}

	return &EventConsumer{conn: conn, ch: ch, store: store}, nil
}

// Close 关闭事件消费者持有的 channel 和 connection。
func (c *EventConsumer) Close() error {
	if c == nil {
		return nil
	}
	if c.ch != nil {
		_ = c.ch.Close()
	}
	if c.conn != nil {
		return c.conn.Close()
	}
	return nil
}

// Run 启动事件消费循环，直到 context 取消或队列通道关闭。
func (c *EventConsumer) Run(ctx context.Context) error {
	deliveries, err := c.ch.Consume(
		EventPersistQueue,
		"",
		false,
		false,
		false,
		false,
		nil,
	)
	if err != nil {
		return fmt.Errorf("consume event persist queue: %w", err)
	}

	slog.Info("gateway consuming event persist queue", "queue", EventPersistQueue)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case delivery, ok := <-deliveries:
			if !ok {
				return fmt.Errorf("event delivery channel closed")
			}
			c.handleDelivery(ctx, delivery)
		}
	}
}

// handleDelivery 解码并持久化单条 RunEvent。
func (c *EventConsumer) handleDelivery(ctx context.Context, delivery amqp.Delivery) {
	var evt event.RunEvent
	if err := json.Unmarshal(delivery.Body, &evt); err != nil {
		slog.Error("decode run event failed", "err", err)
		_ = delivery.Nack(false, false)
		return
	}

	if err := c.store.AppendRunEvent(ctx, evt); err != nil {
		slog.Error("persist run event failed", "run_id", evt.RunID, "seq", evt.Seq, "type", evt.Type, "err", err)
		_ = delivery.Nack(false, false)
		return
	}

	if err := delivery.Ack(false); err != nil {
		slog.Error("ack run event failed", "run_id", evt.RunID, "seq", evt.Seq, "type", evt.Type, "err", err)
		return
	}
	slog.Info("run event persisted", "run_id", evt.RunID, "seq", evt.Seq, "type", evt.Type)
}
