package queue

import (
	"fmt"
	"log/slog"
	"strings"

	amqp "github.com/rabbitmq/amqp091-go"
)

const (
	// Gateway 会把新任务先发到这个 exchange。
	// 现在只有一个任务队列，所以先用一个固定 routing key。
	// 后面如果要按 agent_id、优先级拆队列，可以继续增加 routing key。
	TaskExchange    = "relayflow.task.exchange"
	TaskQueue       = "relayflow.task.queue"
	TaskRoutingKey  = "relayflow.task"
	RetryExchange   = "relayflow.retry.exchange"
	RetryQueue      = "relayflow.retry.queue"
	RetryRoutingKey = "relayflow.retry"
	DLQExchange     = "relayflow.dlq.exchange"
	DLQQueue        = "relayflow.dlq"
	DLQRoutingKey   = "relayflow.dlq"

	EventExchange         = "relayflow.event.exchange"
	EventPersistQueue     = "relayflow.event.persist.queue"
	EventAllRunRoutingKey = "run.#.event"
)

// DeclareTaskTopology 声明 RelayFlow 的任务交换机、任务队列和绑定关系。
//
// Gateway 和 Worker 都会在启动时执行一次声明。RabbitMQ 的 exchange/queue 声明是幂等的：
// 参数一致时重复声明不会破坏已有资源；参数不一致时会返回错误，帮助我们尽早发现部署漂移。
func DeclareTaskTopology(rabbitMQURL string) error {
	// 这里连接 RabbitMQ 只是为了创建 exchange、queue 和绑定关系。
	// 创建完成就关闭连接；真正发布任务、消费任务时会再建立长期连接。
	conn, err := amqp.Dial(rabbitMQURL)
	if err != nil {
		return fmt.Errorf("dial rabbitmq: %w", err)
	}
	defer conn.Close()

	ch, err := conn.Channel()
	if err != nil {
		return fmt.Errorf("open rabbitmq channel: %w", err)
	}
	defer ch.Close()

	if err := declareTaskTopologyWithChannel(ch); err != nil {
		return err
	}

	slog.Info("rabbitmq task topology declared",
		"exchange", TaskExchange,
		"queue", TaskQueue,
		"routing_key", TaskRoutingKey,
	)
	return nil
}

// DeclareEventTopology 声明 RelayFlow 的事件交换机和持久化队列。
// 事件使用 topic exchange，后续可按 run_id 精确订阅，也可按通配符消费全部事件。
func DeclareEventTopology(rabbitMQURL string) error {
	return declareEventResources(rabbitMQURL, true)
}

// DeclareEventExchange 声明事件交换机，供只发布或只广播消费事件的进程使用。
func DeclareEventExchange(rabbitMQURL string) error {
	return declareEventResources(rabbitMQURL, false)
}

// declareEventResources 按进程职责声明事件交换机以及可选的持久化队列。
func declareEventResources(rabbitMQURL string, includePersistQueue bool) error {
	conn, err := amqp.Dial(rabbitMQURL)
	if err != nil {
		return fmt.Errorf("dial rabbitmq: %w", err)
	}
	defer conn.Close()

	ch, err := conn.Channel()
	if err != nil {
		return fmt.Errorf("open rabbitmq channel: %w", err)
	}
	defer ch.Close()

	if err := declareEventExchangeWithChannel(ch); err != nil {
		return err
	}

	if includePersistQueue {
		if err := declareEventPersistQueueWithChannel(ch); err != nil {
			return err
		}

		slog.Info("rabbitmq event topology declared",
			"exchange", EventExchange,
			"persist_queue", EventPersistQueue,
			"routing_key", EventAllRunRoutingKey,
		)
		return nil
	}

	slog.Info("rabbitmq event exchange declared", "exchange", EventExchange)
	return nil
}

// declareTaskTopologyWithChannel 在已有 AMQP channel 上声明任务拓扑。
func declareTaskTopologyWithChannel(ch *amqp.Channel) error {
	if err := ch.ExchangeDeclare(
		TaskExchange,
		"direct",
		true,
		false,
		false,
		false,
		nil,
	); err != nil {
		return fmt.Errorf("declare task exchange: %w", err)
	}

	if _, err := ch.QueueDeclare(
		TaskQueue,
		true,
		false,
		false,
		false,
		nil,
	); err != nil {
		return fmt.Errorf("declare task queue: %w", err)
	} else {
		ObserveQueueMessages(ch, TaskQueue)
	}

	if err := ch.ExchangeDeclare(
		RetryExchange,
		"direct",
		true,
		false,
		false,
		false,
		nil,
	); err != nil {
		return fmt.Errorf("declare retry exchange: %w", err)
	}

	if _, err := ch.QueueDeclare(
		RetryQueue,
		true,
		false,
		false,
		false,
		amqp.Table{
			"x-dead-letter-exchange":    TaskExchange,
			"x-dead-letter-routing-key": TaskRoutingKey,
		},
	); err != nil {
		return fmt.Errorf("declare retry queue: %w", err)
	} else {
		ObserveQueueMessages(ch, RetryQueue)
	}

	if err := ch.QueueBind(
		RetryQueue,
		RetryRoutingKey,
		RetryExchange,
		false,
		nil,
	); err != nil {
		return fmt.Errorf("bind retry queue: %w", err)
	}

	if err := ch.ExchangeDeclare(
		DLQExchange,
		"direct",
		true,
		false,
		false,
		false,
		nil,
	); err != nil {
		return fmt.Errorf("declare dlq exchange: %w", err)
	}

	if _, err := ch.QueueDeclare(
		DLQQueue,
		true,
		false,
		false,
		false,
		nil,
	); err != nil {
		return fmt.Errorf("declare dlq: %w", err)
	} else {
		ObserveQueueMessages(ch, DLQQueue)
	}

	if err := ch.QueueBind(
		DLQQueue,
		DLQRoutingKey,
		DLQExchange,
		false,
		nil,
	); err != nil {
		return fmt.Errorf("bind dlq: %w", err)
	}

	// 把 queue 绑定到 exchange 后，Gateway 发到 exchange 的任务才会进入这个 queue。
	if err := ch.QueueBind(
		TaskQueue,
		TaskRoutingKey,
		TaskExchange,
		false,
		nil,
	); err != nil {
		return fmt.Errorf("bind task queue: %w", err)
	}

	return nil
}

// declareEventTopologyWithChannel 在已有 AMQP channel 上声明事件拓扑。
func declareEventTopologyWithChannel(ch *amqp.Channel) error {
	if err := declareEventExchangeWithChannel(ch); err != nil {
		return err
	}
	return declareEventPersistQueueWithChannel(ch)
}

// declareEventPersistQueueWithChannel 在已有 AMQP channel 上声明事件持久化队列。
func declareEventPersistQueueWithChannel(ch *amqp.Channel) error {
	if _, err := ch.QueueDeclare(
		EventPersistQueue,
		true,
		false,
		false,
		false,
		nil,
	); err != nil {
		return fmt.Errorf("declare event persist queue: %w", err)
	} else {
		ObserveQueueMessages(ch, EventPersistQueue)
	}

	// 持久化队列绑定 run.#.event，用于消费所有 Run 的阶段事件并落 Redis。
	if err := ch.QueueBind(
		EventPersistQueue,
		EventAllRunRoutingKey,
		EventExchange,
		false,
		nil,
	); err != nil {
		return fmt.Errorf("bind event persist queue: %w", err)
	}

	return nil
}

// declareEventExchangeWithChannel 在已有 AMQP channel 上声明事件交换机。
func declareEventExchangeWithChannel(ch *amqp.Channel) error {
	if err := ch.ExchangeDeclare(
		EventExchange,
		"topic",
		true,
		false,
		false,
		false,
		nil,
	); err != nil {
		return fmt.Errorf("declare event exchange: %w", err)
	}
	return nil
}

// EventRoutingKey 返回某个 Run 对应的事件 routing key。
func EventRoutingKey(runID string) string {
	return fmt.Sprintf("run.%s.event", sanitizeRoutingKeyPart(runID))
}

func sanitizeRoutingKeyPart(value string) string {
	return strings.NewReplacer(".", "_", "*", "_", "#", "_").Replace(value)
}
