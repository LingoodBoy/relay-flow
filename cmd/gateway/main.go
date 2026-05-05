package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"time"

	"relay-flow/internal/config"
	gatewayhttp "relay-flow/internal/http"
	"relay-flow/internal/logger"
	"relay-flow/internal/queue"
	"relay-flow/internal/store"
)

// Gateway 负责接收外部 HTTP/SSE 请求，并把任务投递到后端队列。
func main() {
	if err := logger.Init("gateway"); err != nil {
		slog.Error("init logger failed", "err", err)
		os.Exit(1)
	}

	// Gateway/Worker 读取同一套环境变量，保证本地、Docker、生产部署切换时只换配置不换代码。
	cfg, err := config.Load()
	if err != nil {
		slog.Error("load config failed", "err", err)
		os.Exit(1)
	}

	slog.Info("gateway started",
		"rabbitmq", cfg.RabbitMQURL,
		"redis", cfg.RedisAddr,
		"agent", cfg.AgentURL,
		"addr", cfg.GatewayAddr,
		"task_timeout", cfg.TaskTimeout,
	)

	// 拓扑声明放在启动阶段：配置不一致会立刻失败，避免请求进来后才发现队列不可用。
	if err := queue.DeclareTaskTopology(cfg.RabbitMQURL); err != nil {
		slog.Error("declare rabbitmq task topology failed", "err", err)
		os.Exit(1)
	}
	if err := queue.DeclareEventTopology(cfg.RabbitMQURL); err != nil {
		slog.Error("declare rabbitmq event topology failed", "err", err)
		os.Exit(1)
	}

	// Redis 是 Run 状态的第一落点；启动时 Ping 一次，用 ready fail-fast 暴露依赖问题。
	runStore := store.NewRedisStore(cfg.RedisAddr)
	defer runStore.Close()
	ctxRedis, cancelRedis := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancelRedis()
	if err := runStore.Ping(ctxRedis); err != nil {
		slog.Error("connect redis failed", "err", err)
		os.Exit(1)
	}

	// Publisher 持有 RabbitMQ 长连接，避免每个请求都重新建连接造成额外开销。
	taskPublisher, err := queue.NewPublisher(cfg.RabbitMQURL)
	if err != nil {
		slog.Error("create task publisher failed", "err", err)
		os.Exit(1)
	}
	defer taskPublisher.Close()

	server := gatewayhttp.NewServer(gatewayhttp.Dependencies{
		Store:     runStore,
		Publisher: taskPublisher,
	})
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
