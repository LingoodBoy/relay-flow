package observability

import (
	"github.com/prometheus/client_golang/prometheus"
)

var (
	TaskSubmittedTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: "relayflow",
		Name:      "task_submitted_total",
		Help:      "Total number of submitted runs.",
	})

	TaskSucceededTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: "relayflow",
		Name:      "task_succeeded_total",
		Help:      "Total number of succeeded runs.",
	})

	TaskFailedTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: "relayflow",
		Name:      "task_failed_total",
		Help:      "Total number of failed runs.",
	})

	RetryTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: "relayflow",
		Name:      "retry_total",
		Help:      "Total number of retry attempts scheduled by workers.",
	})

	DLQTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: "relayflow",
		Name:      "dlq_total",
		Help:      "Total number of runs sent to the dead letter queue.",
	})

	SSEConnections = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: "relayflow",
		Name:      "sse_connections",
		Help:      "Current number of active SSE connections on this gateway.",
	})

	SSEDisconnectsTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: "relayflow",
		Name:      "sse_disconnects_total",
		Help:      "Total number of SSE connections closed on this gateway.",
	})

	WorkerRunningTasks = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: "relayflow",
		Name:      "worker_running_tasks",
		Help:      "Current number of tasks being executed by this worker.",
	})

	RabbitMQQueueMessages = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "relayflow",
		Name:      "rabbitmq_queue_messages",
		Help:      "RabbitMQ queue backlog observed by RelayFlow processes.",
	}, []string{"queue"})
)

// RegisterMetrics 注册 RelayFlow 业务指标；Go runtime 指标由 Prometheus 默认注册器自动提供。
func RegisterMetrics() {
	prometheus.MustRegister(
		TaskSubmittedTotal,
		TaskSucceededTotal,
		TaskFailedTotal,
		RetryTotal,
		DLQTotal,
		SSEConnections,
		SSEDisconnectsTotal,
		WorkerRunningTasks,
		RabbitMQQueueMessages,
	)
}
