package queue

import (
	"context"
	"encoding/json"
	"fmt"

	amqp "github.com/rabbitmq/amqp091-go"

	"relay-flow/internal/event"
)

// EventPublisher 负责把 RunEvent 发布到 RabbitMQ event exchange。
type EventPublisher struct {
	conn *amqp.Connection
	ch   *amqp.Channel
}

// NewEventPublisher 创建事件发布器，并在 Worker 生命周期内复用连接。
func NewEventPublisher(rabbitMQURL string) (*EventPublisher, error) {
	conn, err := amqp.Dial(rabbitMQURL)
	if err != nil {
		return nil, fmt.Errorf("dial rabbitmq: %w", err)
	}

	ch, err := conn.Channel()
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("open rabbitmq channel: %w", err)
	}

	return &EventPublisher{conn: conn, ch: ch}, nil
}

// Close 关闭事件发布器持有的 channel 和 connection。
func (p *EventPublisher) Close() error {
	if p == nil {
		return nil
	}
	if p.ch != nil {
		_ = p.ch.Close()
	}
	if p.conn != nil {
		return p.conn.Close()
	}
	return nil
}

// PublishRunEvent 把标准 RunEvent 发布到 event exchange。
func (p *EventPublisher) PublishRunEvent(ctx context.Context, evt event.RunEvent) error {
	body, err := json.Marshal(evt)
	if err != nil {
		return fmt.Errorf("marshal run event: %w", err)
	}

	if err := p.ch.PublishWithContext(
		ctx,
		EventExchange,
		EventRoutingKey(evt.RunID),
		false,
		false,
		amqp.Publishing{
			ContentType:  "application/json",
			DeliveryMode: amqp.Persistent,
			MessageId:    fmt.Sprintf("%s:%d", evt.RunID, evt.Seq),
			Timestamp:    evt.CreatedAt,
			Type:         string(evt.Type),
			Body:         body,
		},
	); err != nil {
		return fmt.Errorf("publish run event: %w", err)
	}

	return nil
}
