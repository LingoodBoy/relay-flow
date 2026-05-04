package queue

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

// TaskMessage 是 Gateway 投递给 Worker 的任务消息。
//
// input 保留为 json.RawMessage，是为了让 RelayFlow 不理解、不修改 Agent 的业务入参。
// Agent 需要什么结构，由前端和 Agent 自己约定。
type TaskMessage struct {
	RunID     string          `json:"run_id"`
	AgentID   string          `json:"agent_id"`
	Input     json.RawMessage `json:"input"`
	Cacheable bool            `json:"cacheable"`
	CreatedAt time.Time       `json:"created_at"`
}

type Publisher struct {
	conn *amqp.Connection
	ch   *amqp.Channel
}

// NewPublisher 创建 RabbitMQ 任务发布器，并在 Gateway 生命周期内复用连接。
func NewPublisher(rabbitMQURL string) (*Publisher, error) {
	conn, err := amqp.Dial(rabbitMQURL)
	if err != nil {
		return nil, fmt.Errorf("dial rabbitmq: %w", err)
	}

	ch, err := conn.Channel()
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("open rabbitmq channel: %w", err)
	}

	return &Publisher{conn: conn, ch: ch}, nil
}

// Close 关闭发布器持有的 channel 和 connection。
func (p *Publisher) Close() error {
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

// PublishTask 把任务消息发布到 RabbitMQ task exchange。
func (p *Publisher) PublishTask(ctx context.Context, task TaskMessage) error {
	body, err := json.Marshal(task)
	if err != nil {
		return fmt.Errorf("marshal task message: %w", err)
	}

	// DeliveryMode=Persistent 表示消息写入 durable queue 后，RabbitMQ 会尽量持久化到磁盘。
	// 这和 queue/exchange 的 durable 是两件事：durable 保留队列结构，persistent 保留消息。
	if err := p.ch.PublishWithContext(
		ctx,
		TaskExchange,
		TaskRoutingKey,
		false,
		false,
		amqp.Publishing{
			ContentType:  "application/json",
			DeliveryMode: amqp.Persistent,
			MessageId:    task.RunID,
			Timestamp:    task.CreatedAt,
			Body:         body,
		},
	); err != nil {
		return fmt.Errorf("publish task message: %w", err)
	}

	return nil
}
