package queue

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"go.opentelemetry.io/otel/attribute"

	"relay-flow/internal/observability"
)

const defaultPublisherChannelCount = 16

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
	Attempt   int             `json:"attempt"`
}

type Publisher struct {
	conn     *amqp.Connection
	channels chan *amqp.Channel
	all      []*amqp.Channel
}

// NewPublisher 创建 RabbitMQ 任务发布器，并默认打开 16 个 publish channel。
// AMQP connection 可以复用，但 channel 不适合被多个 goroutine 同时 publish，所以这里用 channel 池提升并发发布能力。
func NewPublisher(rabbitMQURL string) (*Publisher, error) {
	return NewPublisherWithChannelCount(rabbitMQURL, defaultPublisherChannelCount)
}

// NewPublisherWithChannelCount 创建指定大小的 RabbitMQ publish channel 池。
// Gateway 高并发提交任务时会从池里借一个 channel 发布，用完归还，避免所有请求抢同一个 AMQP channel。
func NewPublisherWithChannelCount(rabbitMQURL string, channelCount int) (*Publisher, error) {
	if channelCount <= 0 {
		return nil, fmt.Errorf("publisher channel count must be greater than 0")
	}

	conn, err := amqp.Dial(rabbitMQURL)
	if err != nil {
		return nil, fmt.Errorf("dial rabbitmq: %w", err)
	}

	publisher := &Publisher{
		conn:     conn,
		channels: make(chan *amqp.Channel, channelCount),
		all:      make([]*amqp.Channel, 0, channelCount),
	}

	for i := 0; i < channelCount; i++ {
		ch, err := conn.Channel()
		if err != nil {
			_ = publisher.Close()
			return nil, fmt.Errorf("open rabbitmq channel %d: %w", i+1, err)
		}
		publisher.all = append(publisher.all, ch)
		publisher.channels <- ch
	}

	return publisher, nil
}

// Close 关闭发布器持有的所有 AMQP channel 和 connection。
func (p *Publisher) Close() error {
	if p == nil {
		return nil
	}
	for _, ch := range p.all {
		if ch != nil {
			_ = ch.Close()
		}
	}
	if p.conn != nil {
		return p.conn.Close()
	}
	return nil
}

// borrowChannel 从发布池借出一个 AMQP channel。
// 如果请求上下文已经取消，直接返回错误，避免排队等待 publish channel 时让 HTTP 请求一直挂住。
func (p *Publisher) borrowChannel(ctx context.Context) (*amqp.Channel, error) {
	select {
	case <-ctx.Done():
		return nil, fmt.Errorf("borrow publish channel: %w", ctx.Err())
	case ch := <-p.channels:
		return ch, nil
	}
}

// returnChannel 把借出的 AMQP channel 放回池里。
func (p *Publisher) returnChannel(ch *amqp.Channel) {
	if ch == nil {
		return
	}
	p.channels <- ch
}

// PublishTask 把任务消息发布到 RabbitMQ task exchange。
func (p *Publisher) PublishTask(ctx context.Context, task TaskMessage) error {
	ctx, span := observability.Tracer().Start(ctx, "rabbitmq.publish_task")
	defer span.End()
	span.SetAttributes(attribute.String("run_id", task.RunID), attribute.String("agent_id", task.AgentID))

	if task.Attempt == 0 {
		task.Attempt = 1
	}
	body, err := json.Marshal(task)
	if err != nil {
		return fmt.Errorf("marshal task message: %w", err)
	}

	// DeliveryMode=Persistent 表示消息写入 durable queue 后，RabbitMQ 会尽量持久化到磁盘。
	// 这和 queue/exchange 的 durable 是两件事：durable 保留队列结构，persistent 保留消息。
	ch, err := p.borrowChannel(ctx)
	if err != nil {
		return err
	}
	defer p.returnChannel(ch)
	if err := ch.PublishWithContext(
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
			Headers:      observability.InjectAMQPHeaders(ctx, nil),
			Body:         body,
		},
	); err != nil {
		return fmt.Errorf("publish task message: %w", err)
	}

	return nil
}

// PublishRetryTask 把失败任务投递到 retry queue，消息到期后由 RabbitMQ 自动转回 task queue。
func (p *Publisher) PublishRetryTask(ctx context.Context, task TaskMessage, delay time.Duration) error {
	ctx, span := observability.Tracer().Start(ctx, "rabbitmq.publish_retry_task")
	defer span.End()
	span.SetAttributes(
		attribute.String("run_id", task.RunID),
		attribute.Int("attempt", task.Attempt),
		attribute.Int64("delay_ms", delay.Milliseconds()),
	)

	body, err := json.Marshal(task)
	if err != nil {
		return fmt.Errorf("marshal retry task message: %w", err)
	}

	ch, err := p.borrowChannel(ctx)
	if err != nil {
		return err
	}
	defer p.returnChannel(ch)
	if err := ch.PublishWithContext(
		ctx,
		RetryExchange,
		RetryRoutingKey,
		false,
		false,
		amqp.Publishing{
			ContentType:  "application/json",
			DeliveryMode: amqp.Persistent,
			MessageId:    task.RunID,
			Timestamp:    time.Now().UTC(),
			Expiration:   fmt.Sprintf("%d", delay.Milliseconds()),
			Headers:      observability.InjectAMQPHeaders(ctx, nil),
			Body:         body,
		},
	); err != nil {
		return fmt.Errorf("publish retry task message: %w", err)
	}

	return nil
}

// PublishDeadLetterTask 把不再重试的任务投递到 DLQ，方便后续人工排查或补偿。
func (p *Publisher) PublishDeadLetterTask(ctx context.Context, task TaskMessage, reason string) error {
	ctx, span := observability.Tracer().Start(ctx, "rabbitmq.publish_dead_letter")
	defer span.End()
	span.SetAttributes(attribute.String("run_id", task.RunID), attribute.Int("attempt", task.Attempt))

	body, err := json.Marshal(task)
	if err != nil {
		return fmt.Errorf("marshal dead letter task message: %w", err)
	}

	ch, err := p.borrowChannel(ctx)
	if err != nil {
		return err
	}
	defer p.returnChannel(ch)
	if err := ch.PublishWithContext(
		ctx,
		DLQExchange,
		DLQRoutingKey,
		false,
		false,
		amqp.Publishing{
			ContentType:  "application/json",
			DeliveryMode: amqp.Persistent,
			MessageId:    task.RunID,
			Timestamp:    time.Now().UTC(),
			Headers: observability.InjectAMQPHeaders(ctx, amqp.Table{
				"reason": reason,
			}),
			Body: body,
		},
	); err != nil {
		return fmt.Errorf("publish dead letter task message: %w", err)
	}

	return nil
}
