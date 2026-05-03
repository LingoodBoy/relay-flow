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

	// 声明mq task相关初始化操作
	if err := queue.DeclareTaskTopology(cfg.RabbitMQURL); err != nil {
		slog.Error("declare rabbitmq task topology failed", "err", err)
		os.Exit(1)
	}

	// 初始化redis
	runStore := store.NewRedisStore(cfg.RedisAddr)
	defer runStore.Close()
	ctxRedis, cancelRedis := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancelRedis()
	if err := runStore.Ping(ctxRedis); err != nil {
		slog.Error("connect redis failed", "err", err)
		os.Exit(1)
	}

	// 初始化mq生产者
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
