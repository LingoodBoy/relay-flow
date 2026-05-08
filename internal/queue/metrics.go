package queue

import (
	"log/slog"

	amqp "github.com/rabbitmq/amqp091-go"

	"relay-flow/internal/observability"
)

// ObserveQueueMessages 读取队列当前 ready 消息数并更新 Prometheus gauge。
// RabbitMQ exporter 更适合生产全量监控；这里保留轻量采样，方便本项目直接暴露核心队列堆积。
func ObserveQueueMessages(ch *amqp.Channel, queueName string) {
	q, err := ch.QueueInspect(queueName)
	if err != nil {
		slog.Error("observe rabbitmq queue failed", "queue", queueName, "err", err)
		return
	}
	observability.RabbitMQQueueMessages.WithLabelValues(queueName).Set(float64(q.Messages))
}
