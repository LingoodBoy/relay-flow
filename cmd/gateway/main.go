package main

import (
	"log/slog"
	"os"

	"relay-flow/internal/config"
	"relay-flow/internal/logger"
)

// Gateway 负责接收外部 HTTP/SSE 请求，并把任务投递到后端队列。
func main() {
	if err := logger.Init("gateway"); err != nil {
		slog.Error("init logger failed", "err", err)
		os.Exit(1)
	}

	// 加载config环境变量
	cfg, err := config.Load()
	if err != nil {
		slog.Error("load config failed", "err", err)
		os.Exit(1)
	}

	// 打印环境变量
	slog.Info("gateway started",
		"rabbitmq", cfg.RabbitMQURL,
		"redis", cfg.RedisAddr,
		"agent", cfg.AgentURL,
		"task_timeout", cfg.TaskTimeout,
	)
}
