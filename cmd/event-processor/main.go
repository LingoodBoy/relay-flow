package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"relay-flow/internal/config"
	"relay-flow/internal/logger"
	"relay-flow/internal/observability"
	"relay-flow/internal/queue"
	"relay-flow/internal/store"
)

// EventProcessor 负责消费 RunEvent 持久化队列，并推进 Redis 中的 Run 状态。
func main() {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "load config failed:", err)
		os.Exit(1)
	}

	if err := logger.InitWithOptions(logger.Options{
		Service: "event-processor",
		LogDir:  "logs",
		Level:   cfg.LogLevel,
	}); err != nil {
		fmt.Fprintln(os.Stderr, "init logger failed:", err)
		os.Exit(1)
	}
	observability.RegisterMetrics()
	shutdownTracing, err := observability.InitTracing(context.Background(), "relayflow-event-processor")
	if err != nil {
		slog.Error("init tracing failed", "err", err)
		os.Exit(1)
	}
	defer func() {
		if err := shutdownTracing(context.Background()); err != nil {
			slog.Error("shutdown tracing failed", "err", err)
		}
	}()

	slog.Info("event processor started",
		"rabbitmq", cfg.RabbitMQURL,
		"redis", cfg.RedisAddr,
		"metrics_addr", cfg.ProcessorMetricsAddr,
	)
	go serveMetrics(cfg.ProcessorMetricsAddr)

	// EventProcessor 独立声明事件拓扑，保证它可以不依赖 Relay API 启动顺序。
	if err := queue.DeclareEventTopology(cfg.RabbitMQURL); err != nil {
		slog.Error("declare rabbitmq event topology failed", "err", err)
		os.Exit(1)
	}

	runStore := store.NewRedisStore(cfg.RedisAddr)
	defer runStore.Close()
	ctxRedis, cancelRedis := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancelRedis()
	if err := runStore.Ping(ctxRedis); err != nil {
		slog.Error("connect redis failed", "err", err)
		os.Exit(1)
	}

	consumer, err := queue.NewPersistEventConsumer(cfg.RabbitMQURL, runStore)
	if err != nil {
		slog.Error("create persist event consumer failed", "err", err)
		os.Exit(1)
	}
	defer consumer.Close()

	if err := consumer.Run(context.Background()); err != nil {
		slog.Error("persist event consumer stopped", "err", err)
		os.Exit(1)
	}
}

// serveMetrics 为 EventProcessor 暴露 Prometheus 指标，便于观察事件持久化结果。
func serveMetrics(addr string) {
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	if err := http.ListenAndServe(addr, mux); err != nil {
		slog.Error("event processor metrics server stopped", "err", err)
	}
}
