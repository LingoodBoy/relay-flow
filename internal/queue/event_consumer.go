package queue

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	amqp "github.com/rabbitmq/amqp091-go"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"

	"relay-flow/internal/event"
	"relay-flow/internal/logger"
	"relay-flow/internal/observability"
)

type RunEventStore interface {
	AppendRunEvent(ctx context.Context, evt event.RunEvent) error
}

type RunEventSink interface {
	PublishRunEvent(evt event.RunEvent)
}

// PersistEventConsumer 消费持久化事件队列，并把 RunEvent 写入状态存储。
type PersistEventConsumer struct {
	conn  *amqp.Connection
	ch    *amqp.Channel
	store RunEventStore
}

// BroadcastEventConsumer 为当前 Relay API 实例创建临时广播队列，并把事件推给本机 SSEHub。
type BroadcastEventConsumer struct {
	conn  *amqp.Connection
	ch    *amqp.Channel
	queue string
	sink  RunEventSink
}

// NewPersistEventConsumer 创建持久化事件消费者。
func NewPersistEventConsumer(rabbitMQURL string, store RunEventStore) (*PersistEventConsumer, error) {
	conn, err := amqp.Dial(rabbitMQURL)
	if err != nil {
		return nil, fmt.Errorf("dial rabbitmq: %w", err)
	}

	ch, err := conn.Channel()
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("open rabbitmq channel: %w", err)
	}

	return &PersistEventConsumer{conn: conn, ch: ch, store: store}, nil
}

// NewBroadcastEventConsumer 创建当前实例专属的事件广播消费者。
func NewBroadcastEventConsumer(rabbitMQURL string, sink RunEventSink) (*BroadcastEventConsumer, error) {
	conn, err := amqp.Dial(rabbitMQURL)
	if err != nil {
		return nil, fmt.Errorf("dial rabbitmq: %w", err)
	}

	ch, err := conn.Channel()
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("open rabbitmq channel: %w", err)
	}
	if err := declareEventExchangeWithChannel(ch); err != nil {
		_ = ch.Close()
		_ = conn.Close()
		return nil, err
	}

	q, err := ch.QueueDeclare(
		"",
		false,
		true,
		true,
		false,
		nil,
	)
	if err != nil {
		_ = ch.Close()
		_ = conn.Close()
		return nil, fmt.Errorf("declare event broadcast queue: %w", err)
	}

	if err := ch.QueueBind(
		q.Name,
		EventAllRunRoutingKey,
		EventExchange,
		false,
		nil,
	); err != nil {
		_ = ch.Close()
		_ = conn.Close()
		return nil, fmt.Errorf("bind event broadcast queue: %w", err)
	}

	return &BroadcastEventConsumer{conn: conn, ch: ch, queue: q.Name, sink: sink}, nil
}

// Close 关闭持久化事件消费者持有的 channel 和 connection。
func (c *PersistEventConsumer) Close() error {
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

// Close 关闭广播事件消费者持有的 channel 和 connection。
func (c *BroadcastEventConsumer) Close() error {
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

// Run 启动持久化事件消费循环，直到 context 取消或队列通道关闭。
func (c *PersistEventConsumer) Run(ctx context.Context) error {
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

	slog.Info("event processor consuming persist queue", "queue", EventPersistQueue)
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

// Run 启动广播事件消费循环，直到 context 取消或队列通道关闭。
func (c *BroadcastEventConsumer) Run(ctx context.Context) error {
	deliveries, err := c.ch.Consume(
		c.queue,
		"",
		false,
		true,
		false,
		false,
		nil,
	)
	if err != nil {
		return fmt.Errorf("consume event broadcast queue: %w", err)
	}

	slog.Info("relay api consuming event broadcast queue", "queue", c.queue)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case delivery, ok := <-deliveries:
			if !ok {
				return fmt.Errorf("event broadcast delivery channel closed")
			}
			c.handleDelivery(ctx, delivery)
		}
	}
}

// handleDelivery 解码并持久化单条 RunEvent。
func (c *PersistEventConsumer) handleDelivery(ctx context.Context, delivery amqp.Delivery) {
	ctx = observability.ExtractAMQPContext(ctx, delivery.Headers)
	ctx, span := observability.Tracer().Start(ctx, "event_processor.persist_run_event")
	defer span.End()

	evt, err := decodeRunEventDelivery(delivery)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "decode run event failed")
		logger.ErrorContext(ctx, "decode run event failed", "err", err)
		_ = delivery.Nack(false, false)
		return
	}
	span.SetAttributes(
		attribute.String("run_id", evt.RunID),
		attribute.Int64("event.seq", evt.Seq),
		attribute.String("event.type", string(evt.Type)),
	)

	if err := c.store.AppendRunEvent(ctx, evt); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "persist run event failed")
		logger.ErrorContext(ctx, "persist run event failed", "run_id", evt.RunID, "seq", evt.Seq, "type", evt.Type, "err", err)
		_ = delivery.Nack(false, false)
		return
	}

	if err := delivery.Ack(false); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "ack run event failed")
		logger.ErrorContext(ctx, "ack run event failed", "run_id", evt.RunID, "seq", evt.Seq, "type", evt.Type, "err", err)
		return
	}
	switch evt.Type {
	case event.EventTypeSucceeded:
		observability.TaskSucceededTotal.Inc()
	case event.EventTypeFailed:
		observability.TaskFailedTotal.Inc()
	case event.EventTypeDeadLetter:
		observability.TaskFailedTotal.Inc()
		observability.DLQTotal.Inc()
	}
	slog.Debug("run event persisted", "run_id", evt.RunID, "seq", evt.Seq, "type", evt.Type)
}

// handleDelivery 解码单条广播事件，并分发给当前进程内的 SSE 订阅者。
func (c *BroadcastEventConsumer) handleDelivery(ctx context.Context, delivery amqp.Delivery) {
	ctx = observability.ExtractAMQPContext(ctx, delivery.Headers)
	ctx, span := observability.Tracer().Start(ctx, "relay_api.broadcast_run_event")
	defer span.End()

	evt, err := decodeRunEventDelivery(delivery)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "decode run event failed")
		logger.ErrorContext(ctx, "decode broadcast run event failed", "err", err)
		_ = delivery.Nack(false, false)
		return
	}
	span.SetAttributes(
		attribute.String("run_id", evt.RunID),
		attribute.Int64("event.seq", evt.Seq),
		attribute.String("event.type", string(evt.Type)),
	)

	if c.sink != nil {
		c.sink.PublishRunEvent(evt)
	}
	if err := delivery.Ack(false); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "ack broadcast run event failed")
		logger.ErrorContext(ctx, "ack broadcast run event failed", "run_id", evt.RunID, "seq", evt.Seq, "type", evt.Type, "err", err)
		return
	}
	slog.Debug("run event broadcasted", "run_id", evt.RunID, "seq", evt.Seq, "type", evt.Type)
}

// decodeRunEventDelivery 把 RabbitMQ delivery 反序列化为标准 RunEvent。
func decodeRunEventDelivery(delivery amqp.Delivery) (event.RunEvent, error) {
	var evt event.RunEvent
	if err := json.Unmarshal(delivery.Body, &evt); err != nil {
		return event.RunEvent{}, err
	}
	return evt, nil
}
