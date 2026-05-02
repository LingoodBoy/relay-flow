package main

import (
	"log/slog"
	"os"

	"relay-flow/internal/config"
	"relay-flow/internal/logger"
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

	// Phase 0 先验证进程启动和配置读取，后续再接入 RabbitMQ consumer 和 Agent client。
	slog.Info("worker started",
		"rabbitmq", cfg.RabbitMQURL,
		"redis", cfg.RedisAddr,
		"agent", cfg.AgentURL,
		"task_timeout", cfg.TaskTimeout,
	)
}
