package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"

	"relay-flow/internal/event"
	"relay-flow/internal/logger"
	"relay-flow/internal/observability"
	"relay-flow/internal/queue"
)

type Consumer struct {
	conn           *amqp.Connection
	ch             *amqp.Channel
	agentClient    *AgentClient
	eventPublisher *queue.EventPublisher
	taskPublisher  *queue.Publisher
	concurrency    int
	maxAttempts    int
	ackMu          sync.Mutex
}

// NewConsumer 创建 RabbitMQ 任务消费者，并持有消费用的长连接。
func NewConsumer(rabbitMQURL string, agentClient *AgentClient, eventPublisher *queue.EventPublisher, taskPublisher *queue.Publisher, concurrency int, maxAttempts int) (*Consumer, error) {
	conn, err := amqp.Dial(rabbitMQURL)
	if err != nil {
		return nil, fmt.Errorf("dial rabbitmq: %w", err)
	}

	ch, err := conn.Channel()
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("open rabbitmq channel: %w", err)
	}
	if err := ch.Qos(concurrency, 0, false); err != nil {
		_ = ch.Close()
		_ = conn.Close()
		return nil, fmt.Errorf("set rabbitmq qos: %w", err)
	}

	return &Consumer{
		conn:           conn,
		ch:             ch,
		agentClient:    agentClient,
		eventPublisher: eventPublisher,
		taskPublisher:  taskPublisher,
		concurrency:    concurrency,
		maxAttempts:    maxAttempts,
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

// Run 启动任务消费循环，并限制同一进程内同时执行的任务数。
func (c *Consumer) Run(ctx context.Context) error {
	// autoAck=false，表示 Worker 必须处理成功后手动 ACK。
	// 这样 Worker 异常退出时，RabbitMQ 不会把未确认的任务当成已完成。
	// Qos(prefetch=concurrency) 会让超过并发上限的消息继续留在 RabbitMQ 队列中。
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

	sem := make(chan struct{}, c.concurrency)
	slog.Info("worker consuming task queue", "queue", queue.TaskQueue, "concurrency", c.concurrency)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case delivery, ok := <-deliveries:
			if !ok {
				return fmt.Errorf("task delivery channel closed")
			}
			queue.ObserveQueueMessages(c.ch, queue.TaskQueue)
			sem <- struct{}{}
			go func() {
				defer func() { <-sem }()
				c.handleDelivery(ctx, delivery)
			}()
		}
	}
}

// handleDelivery 处理单条任务消息，并根据执行结果 ACK 或 NACK。
func (c *Consumer) handleDelivery(ctx context.Context, delivery amqp.Delivery) {
	ctx = observability.ExtractAMQPContext(ctx, delivery.Headers)
	ctx, span := observability.Tracer().Start(ctx, "worker.consume_task")
	defer span.End()
	observability.WorkerRunningTasks.Inc()
	defer observability.WorkerRunningTasks.Dec()

	var task queue.TaskMessage
	if err := json.Unmarshal(delivery.Body, &task); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "decode task message failed")
		// 消息格式错误通常不是临时故障，重新入队也大概率继续失败，所以直接丢弃。
		slog.Error("decode task message failed", "err", err)
		c.nackDelivery(delivery, false)
		return
	}
	if task.Attempt == 0 {
		task.Attempt = 1
	}
	span.SetAttributes(
		attribute.String("run_id", task.RunID),
		attribute.String("agent_id", task.AgentID),
		attribute.Int("attempt", task.Attempt),
	)

	slog.Debug("task received", "run_id", task.RunID, "agent_id", task.AgentID, "attempt", task.Attempt)
	baseSeq := attemptBaseSeq(task.Attempt)
	if err := c.publishRunEvent(ctx, task.RunID, baseSeq, event.EventTypeRunning, "任务开始执行", nil); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "publish running event failed")
		slog.Error("publish running event failed", "run_id", task.RunID, "err", err)
		c.nackDelivery(delivery, false)
		return
	}

	adapter := NewEventAdapter(task.RunID, baseSeq+1)
	if err := c.agentClient.StreamEvents(ctx, task, func(raw AgentRawEvent) error {
		evt, ok, err := adapter.Adapt(raw)
		if err != nil {
			return err
		}
		if !ok {
			slog.Debug("agent event ignored", "run_id", task.RunID, "event_type", raw.Type)
			return nil
		}
		if err := c.eventPublisher.PublishRunEvent(ctx, evt); err != nil {
			return fmt.Errorf("publish adapted run event: %w", err)
		}
		slog.Debug("agent event published", "run_id", task.RunID, "seq", evt.Seq, "type", evt.Type)
		return nil
	}); err != nil {
		span.RecordError(err)
		logger.ErrorContext(ctx, "task execution failed", "run_id", task.RunID, "attempt", task.Attempt, "err", err)
		if errors.Is(err, ErrAgentFailedEvent) {
			slog.Debug("agent failed event already published", "run_id", task.RunID)
			c.nackDelivery(delivery, false)
			return
		}
		c.handleTaskFailure(ctx, delivery, task, adapter.NextSeq(), err)
		return
	}

	// 只有 Agent 成功返回后才 ACK，确保队列语义是“至少处理到 Agent 调用完成”。
	if err := c.ackDelivery(delivery); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "ack task failed")
		logger.ErrorContext(ctx, "ack task failed", "run_id", task.RunID, "err", err)
		return
	}
	slog.Debug("task acked", "run_id", task.RunID)
}

// ackDelivery 串行化 ACK，避免多个 goroutine 同时操作同一个 AMQP channel。
func (c *Consumer) ackDelivery(delivery amqp.Delivery) error {
	c.ackMu.Lock()
	defer c.ackMu.Unlock()
	return delivery.Ack(false)
}

// nackDelivery 串行化 NACK，requeue=false 表示当前阶段失败消息不重新入队。
func (c *Consumer) nackDelivery(delivery amqp.Delivery, requeue bool) {
	c.ackMu.Lock()
	defer c.ackMu.Unlock()
	_ = delivery.Nack(false, requeue)
}

// handleTaskFailure 根据错误类型决定补发事件、延迟重试或进入死信队列。
func (c *Consumer) handleTaskFailure(ctx context.Context, delivery amqp.Delivery, task queue.TaskMessage, seq int64, err error) {
	ctx, span := observability.Tracer().Start(ctx, "worker.handle_task_failure")
	defer span.End()
	span.SetAttributes(attribute.String("run_id", task.RunID), attribute.Int("attempt", task.Attempt))

	errKind := classifyAgentError(err)
	canRetry := isRetryableAgentError(errKind) && task.Attempt < c.maxAttempts
	if errKind == agentErrorTimeout {
		if publishErr := c.publishErrorEvent(ctx, task.RunID, seq, event.EventTypeTimeout, "Agent 调用超时", err); publishErr != nil {
			logger.ErrorContext(ctx, "publish timeout event failed", "run_id", task.RunID, "err", publishErr)
		}
		seq++
	}

	if !canRetry {
		// Worker 自己判定不可重试或重试耗尽时，dead_letter 就是最终失败事件。
		// 这里不再额外发布 failed，避免同一个 Run 同时出现 failed + dead_letter 后被指标双计数。
		if publishErr := c.publishDeadLetter(ctx, task, seq, err); publishErr != nil {
			logger.ErrorContext(ctx, "publish dead letter failed", "run_id", task.RunID, "err", publishErr)
			c.nackDelivery(delivery, true)
			return
		}
		c.nackDelivery(delivery, false)
		return
	}

	nextTask := task
	nextTask.Attempt++
	delay := retryDelay(task.Attempt)
	if publishErr := c.publishRetrying(ctx, nextTask, seq, delay, err); publishErr != nil {
		span.RecordError(publishErr)
		logger.ErrorContext(ctx, "publish retrying failed", "run_id", task.RunID, "err", publishErr)
		c.nackDelivery(delivery, true)
		return
	}
	if err := c.taskPublisher.PublishRetryTask(ctx, nextTask, delay); err != nil {
		span.RecordError(err)
		logger.ErrorContext(ctx, "publish retry task failed", "run_id", task.RunID, "err", err)
		c.nackDelivery(delivery, true)
		return
	}

	c.ackOrLog(delivery, task.RunID)
	observability.RetryTotal.Inc()
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

// publishErrorEvent 把错误包装成指定类型事件，timeout/failed 共用这一条路径。
func (c *Consumer) publishErrorEvent(ctx context.Context, runID string, seq int64, eventType event.EventType, message string, err error) error {
	payload, marshalErr := json.Marshal(map[string]string{"error": err.Error()})
	if marshalErr != nil {
		return fmt.Errorf("marshal error event payload: %w", marshalErr)
	}

	return c.publishRunEvent(ctx, runID, seq, eventType, message, payload)
}

// publishRetrying 发布重试计划事件，让查询接口和 SSE 都能看到任务并非静默等待。
func (c *Consumer) publishRetrying(ctx context.Context, task queue.TaskMessage, seq int64, delay time.Duration, err error) error {
	payload, marshalErr := json.Marshal(map[string]any{
		"error":        err.Error(),
		"next_attempt": task.Attempt,
		"delay_ms":     delay.Milliseconds(),
	})
	if marshalErr != nil {
		return fmt.Errorf("marshal retrying event payload: %w", marshalErr)
	}

	return c.publishRunEvent(ctx, task.RunID, seq, event.EventTypeRetrying, "任务稍后重试", payload)
}

// publishDeadLetter 发布最终死信事件，并把任务消息写入 RabbitMQ DLQ。
func (c *Consumer) publishDeadLetter(ctx context.Context, task queue.TaskMessage, seq int64, err error) error {
	payload, marshalErr := json.Marshal(map[string]any{
		"error":        err.Error(),
		"attempt":      task.Attempt,
		"max_attempts": c.maxAttempts,
	})
	if marshalErr != nil {
		return fmt.Errorf("marshal dead letter event payload: %w", marshalErr)
	}
	if err := c.publishRunEvent(ctx, task.RunID, seq, event.EventTypeDeadLetter, "任务进入死信队列", payload); err != nil {
		return err
	}
	return c.taskPublisher.PublishDeadLetterTask(ctx, task, err.Error())
}

// ackOrLog 确认当前消息已经由 Worker 接管完成，ACK 失败只记录日志。
func (c *Consumer) ackOrLog(delivery amqp.Delivery, runID string) {
	if err := c.ackDelivery(delivery); err != nil {
		slog.Error("ack task failed", "run_id", runID, "err", err)
	}
}

type agentErrorKind string

const (
	agentErrorNetwork agentErrorKind = "network"
	agentErrorTimeout agentErrorKind = "timeout"
	agentError5xx     agentErrorKind = "agent_5xx"
	agentError4xx     agentErrorKind = "agent_4xx"
	agentErrorInvalid agentErrorKind = "invalid"
	agentErrorUnknown agentErrorKind = "unknown"
)

// classifyAgentError 把 Agent 调用错误分成可重试和不可重试的大类。
func classifyAgentError(err error) agentErrorKind {
	if errors.Is(err, context.DeadlineExceeded) {
		return agentErrorTimeout
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		if netErr.Timeout() {
			return agentErrorTimeout
		}
		return agentErrorNetwork
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return agentErrorNetwork
	}
	var statusErr AgentStatusError
	if errors.As(err, &statusErr) {
		if statusErr.StatusCode >= http.StatusInternalServerError {
			return agentError5xx
		}
		if statusErr.StatusCode >= http.StatusBadRequest {
			return agentError4xx
		}
	}
	var invalidErr AgentInvalidRequestError
	if errors.As(err, &invalidErr) {
		return agentErrorInvalid
	}
	return agentErrorUnknown
}

// isRetryableAgentError 判断某类错误是否值得延迟重试。
func isRetryableAgentError(kind agentErrorKind) bool {
	switch kind {
	case agentErrorNetwork, agentErrorTimeout, agentError5xx:
		return true
	default:
		return false
	}
}

// retryDelay 根据当前 attempt 生成退避时间，避免失败任务立刻打回 Agent。
func retryDelay(attempt int) time.Duration {
	if attempt <= 1 {
		return 10 * time.Second
	}
	if attempt == 2 {
		return 30 * time.Second
	}
	return time.Minute
}

// attemptBaseSeq 为每次执行尝试预留一段 seq，保证重试后的事件仍然单调递增。
func attemptBaseSeq(attempt int) int64 {
	if attempt <= 1 {
		return 1
	}
	return int64((attempt-1)*1000 + 1)
}
