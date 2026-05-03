package queue

import (
	"fmt"
	"log/slog"

	amqp "github.com/rabbitmq/amqp091-go"
)

const (
	// Gateway 会把新任务先发到这个 exchange。
	// 现在只有一个任务队列，所以先用一个固定 routing key。
	// 后面如果要按 agent_id、优先级拆队列，可以继续增加 routing key。
	TaskExchange   = "relayflow.task.exchange"
	TaskQueue      = "relayflow.task.queue"
	TaskRoutingKey = "relayflow.task"
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
