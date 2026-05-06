package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	"relay-flow/internal/event"
	"relay-flow/internal/queue"
)

type Consumer struct {
	conn           *amqp.Connection
	ch             *amqp.Channel
	agentClient    *AgentClient
	eventPublisher *queue.EventPublisher
}

// NewConsumer 创建 RabbitMQ 任务消费者，并持有消费用的长连接。
func NewConsumer(rabbitMQURL string, agentClient *AgentClient, eventPublisher *queue.EventPublisher) (*Consumer, error) {
	conn, err := amqp.Dial(rabbitMQURL)
	if err != nil {
		return nil, fmt.Errorf("dial rabbitmq: %w", err)
	}

	ch, err := conn.Channel()
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("open rabbitmq channel: %w", err)
	}

	return &Consumer{
		conn:           conn,
		ch:             ch,
		agentClient:    agentClient,
		eventPublisher: eventPublisher,
	}, nil
}

// Close 关闭 Consumer 持有的 AMQP channel 和 connection。
func (c *Consumer) Close() error {
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

// Run 启动任务消费循环，直到 context 取消或队列通道关闭。
func (c *Consumer) Run(ctx context.Context) error {
	// autoAck=false，表示 Worker 必须处理成功后手动 ACK。
	// 这样 Worker 异常退出时，RabbitMQ 不会把未确认的任务当成已完成。
	// 当前阶段只做单 Worker 基础消费，重试和死信队列会放到后续阶段实现。
	deliveries, err := c.ch.Consume(
		queue.TaskQueue,
		"",
		false,
		false,
		false,
		false,
		nil,
	)
	if err != nil {
		return fmt.Errorf("consume task queue: %w", err)
	}

	slog.Info("worker consuming task queue", "queue", queue.TaskQueue)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case delivery, ok := <-deliveries:
			if !ok {
				return fmt.Errorf("task delivery channel closed")
			}
			c.handleDelivery(ctx, delivery)
		}
	}
}

// handleDelivery 处理单条任务消息，并根据执行结果 ACK 或 NACK。
func (c *Consumer) handleDelivery(ctx context.Context, delivery amqp.Delivery) {
	var task queue.TaskMessage
	if err := json.Unmarshal(delivery.Body, &task); err != nil {
		// 消息格式错误通常不是临时故障，重新入队也大概率继续失败，所以直接丢弃。
		slog.Error("decode task message failed", "err", err)
		_ = delivery.Nack(false, false)
		return
	}

	slog.Info("task received", "run_id", task.RunID, "agent_id", task.AgentID)
	if err := c.publishRunEvent(ctx, task.RunID, 1, event.EventTypeRunning, "任务开始执行", nil); err != nil {
		slog.Error("publish running event failed", "run_id", task.RunID, "err", err)
		_ = delivery.Nack(false, false)
		return
	}

	adapter := NewEventAdapter(task.RunID, 2)
	if err := c.agentClient.StreamEvents(ctx, task, func(raw AgentRawEvent) error {
		evt, ok, err := adapter.Adapt(raw)
		if err != nil {
			return err
		}
		if !ok {
			slog.Info("agent event ignored", "run_id", task.RunID, "event_type", raw.Type)
			return nil
		}
		if err := c.eventPublisher.PublishRunEvent(ctx, evt); err != nil {
			return fmt.Errorf("publish adapted run event: %w", err)
		}
		slog.Info("agent event published", "run_id", task.RunID, "seq", evt.Seq, "type", evt.Type)
		return nil
	}); err != nil {
		// Phase 3 仍然先丢弃失败消息；重试和死信队列后续单独设计，避免现在无限重放。
		slog.Error("task execution failed", "run_id", task.RunID, "err", err)
		if !errors.Is(err, ErrAgentFailedEvent) {
			if publishErr := c.publishFailedEvent(ctx, task.RunID, adapter.NextSeq(), err); publishErr != nil {
				slog.Error("publish failed event failed", "run_id", task.RunID, "err", publishErr)
			}
		} else {
			slog.Info("agent failed event already published", "run_id", task.RunID)
		}
		_ = delivery.Nack(false, false)
		return
	}

	// 只有 Agent 成功返回后才 ACK，确保队列语义是“至少处理到 Agent 调用完成”。
	if err := delivery.Ack(false); err != nil {
		slog.Error("ack task failed", "run_id", task.RunID, "err", err)
		return
	}
	slog.Info("task acked", "run_id", task.RunID)
}

// publishRunEvent 构造并发布 Worker 基础事件。
func (c *Consumer) publishRunEvent(ctx context.Context, runID string, seq int64, eventType event.EventType, message string, payload json.RawMessage) error {
	evt := event.RunEvent{
		RunID:     runID,
		Seq:       seq,
		Type:      eventType,
		Message:   message,
		Payload:   payload,
		CreatedAt: time.Now().UTC(),
	}
	return c.eventPublisher.PublishRunEvent(ctx, evt)
}

// publishFailedEvent 把错误信息包装成标准 failed 事件。
func (c *Consumer) publishFailedEvent(ctx context.Context, runID string, seq int64, err error) error {
	payload, marshalErr := json.Marshal(map[string]string{"error": err.Error()})
	if marshalErr != nil {
		return fmt.Errorf("marshal failed event payload: %w", marshalErr)
	}

	return c.publishRunEvent(ctx, runID, seq, event.EventTypeFailed, "任务执行失败", payload)
}
