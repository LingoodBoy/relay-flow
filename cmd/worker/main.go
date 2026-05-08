package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"relay-flow/internal/config"
	"relay-flow/internal/logger"
	"relay-flow/internal/observability"
	"relay-flow/internal/queue"
	workerpkg "relay-flow/internal/worker"
)

// Worker 负责消费队列任务、调用 Agent 服务，并回传执行状态。
func main() {
	if err := logger.Init("worker"); err != nil {
		slog.Error("init logger failed", "err", err)
		os.Exit(1)
	}
	observability.RegisterMetrics()
	shutdownTracing, err := observability.InitTracing(context.Background(), "relayflow-worker")
	if err != nil {
		slog.Error("init tracing failed", "err", err)
		os.Exit(1)
	}
	defer func() {
		if err := shutdownTracing(context.Background()); err != nil {
			slog.Error("shutdown tracing failed", "err", err)
		}
	}()

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
		"agent_timeout", cfg.AgentTimeout,
		"concurrency", cfg.WorkerConcurrency,
		"max_attempts", cfg.MaxAttempts,
		"metrics_addr", cfg.WorkerMetricsAddr,
	)
	go serveMetrics(cfg.WorkerMetricsAddr)

	// Worker 也声明拓扑是为了支持独立部署：即使 Gateway 暂时没启动，Worker 也能自检队列结构。
	// RabbitMQ 声明是幂等的，参数一致时不会覆盖已有 exchange/queue/message。
	if err := queue.DeclareTaskTopology(cfg.RabbitMQURL); err != nil {
		slog.Error("declare rabbitmq task topology failed", "err", err)
		os.Exit(1)
	}
	if err := queue.DeclareEventTopology(cfg.RabbitMQURL); err != nil {
		slog.Error("declare rabbitmq event topology failed", "err", err)
		os.Exit(1)
	}

	// AgentClient 只关心黑盒 Agent 的 HTTP 协议；Worker 不解析 Agent 的业务输入。
	agentClient := workerpkg.NewAgentClient(cfg.AgentURL, cfg.AgentTimeout)
	eventPublisher, err := queue.NewEventPublisher(cfg.RabbitMQURL)
	if err != nil {
		slog.Error("create event publisher failed", "err", err)
		os.Exit(1)
	}
	defer eventPublisher.Close()

	taskPublisher, err := queue.NewPublisher(cfg.RabbitMQURL)
	if err != nil {
		slog.Error("create task publisher failed", "err", err)
		os.Exit(1)
	}
	defer taskPublisher.Close()

	consumer, err := workerpkg.NewConsumer(cfg.RabbitMQURL, agentClient, eventPublisher, taskPublisher, cfg.WorkerConcurrency, cfg.MaxAttempts)
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

// serveMetrics 为 Worker 暴露 Prometheus 指标，避免 Gateway 与 Worker 指标混在同一个进程端口。
func serveMetrics(addr string) {
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	if err := http.ListenAndServe(addr, mux); err != nil {
		slog.Error("worker metrics server stopped", "err", err)
	}
}
