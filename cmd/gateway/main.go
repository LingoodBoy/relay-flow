package main

import (
	"log/slog"
	"net/http"
	"os"

	"relay-flow/internal/config"
	gatewayhttp "relay-flow/internal/http"
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
		"addr", cfg.GatewayAddr,
		"task_timeout", cfg.TaskTimeout,
	)

	server := gatewayhttp.NewServer()
	httpServer := &http.Server{
		Addr:    cfg.GatewayAddr,
		Handler: server.Handler(),
	}

	slog.Info("gateway http server listening", "addr", cfg.GatewayAddr)
	if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		slog.Error("gateway http server failed", "err", err)
		os.Exit(1)
	}
}
