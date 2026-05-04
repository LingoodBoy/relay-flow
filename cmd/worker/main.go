package main

import (
	"context"
	"log/slog"
	"os"

	"relay-flow/internal/config"
	"relay-flow/internal/logger"
	"relay-flow/internal/queue"
	workerpkg "relay-flow/internal/worker"
)

// Worker 负责消费队列任务、调用 Agent 服务，并回传执行状态。
func main() {
	if err := logger.Init("worker"); err != nil {
		slog.Error("init logger failed", "err", err)
		os.Exit(1)
	}

	// 和 Gateway 复用同一套基础配置，避免两类进程对外部依赖的理解不一致。
	cfg, err := config.Load()
	if err != nil {
		slog.Error("load config failed", "err", err)
		os.Exit(1)
	}

	slog.Info("worker started",
		"rabbitmq", cfg.RabbitMQURL,
		"redis", cfg.RedisAddr,
		"agent", cfg.AgentURL,
		"task_timeout", cfg.TaskTimeout,
	)

	// Worker 也声明拓扑是为了支持独立部署：即使 Gateway 暂时没启动，Worker 也能自检队列结构。
	// RabbitMQ 声明是幂等的，参数一致时不会覆盖已有 exchange/queue/message。
	if err := queue.DeclareTaskTopology(cfg.RabbitMQURL); err != nil {
		slog.Error("declare rabbitmq task topology failed", "err", err)
		os.Exit(1)
	}

	// AgentClient 只关心黑盒 Agent 的 HTTP 协议；Worker 不解析 Agent 的业务输入。
	agentClient := workerpkg.NewAgentClient(cfg.AgentURL, cfg.TaskTimeout)
	consumer, err := workerpkg.NewConsumer(cfg.RabbitMQURL, agentClient)
	if err != nil {
		slog.Error("create worker consumer failed", "err", err)
		os.Exit(1)
	}
	defer consumer.Close()

	if err := consumer.Run(context.Background()); err != nil {
		slog.Error("worker consumer stopped", "err", err)
		os.Exit(1)
	}
}
