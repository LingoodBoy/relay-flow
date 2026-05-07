package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

const (
	// 默认值面向本地开发；Docker Compose 会用服务名覆盖这些地址。
	defaultRabbitMQURL       = "amqp://guest:guest@localhost:5672/"
	defaultRedisAddr         = "localhost:6379"
	defaultAgentHTTPURL      = "http://localhost:8000"
	defaultGatewayAddr       = ":8080"
	defaultTaskTimeoutSecond = 30
	defaultWorkerConcurrency = 20
)

// Config 是 Gateway 和 Worker 共享的基础运行时配置。
type Config struct {
	RabbitMQURL       string
	RedisAddr         string
	AgentURL          string
	GatewayAddr       string
	TaskTimeout       time.Duration
	WorkerConcurrency int
}

// Load 从环境变量加载配置；未设置时使用本地开发默认值。
func Load() (Config, error) {
	// 任务超时必须大于 0，避免配置错误导致 Agent 调用立即超时。
	taskTimeoutSeconds, err := getEnvInt("RELAYFLOW_TASK_TIMEOUT_SECONDS", defaultTaskTimeoutSecond)
	if err != nil {
		return Config{}, err
	}
	workerConcurrency, err := getEnvInt("RELAYFLOW_WORKER_CONCURRENCY", defaultWorkerConcurrency)
	if err != nil {
		return Config{}, err
	}

	return Config{
		RabbitMQURL:       getEnv("RELAYFLOW_RABBITMQ_URL", defaultRabbitMQURL),
		RedisAddr:         getEnv("RELAYFLOW_REDIS_ADDR", defaultRedisAddr),
		AgentURL:          getEnv("RELAYFLOW_AGENT_URL", defaultAgentHTTPURL),
		GatewayAddr:       getEnv("RELAYFLOW_GATEWAY_ADDR", defaultGatewayAddr),
		TaskTimeout:       time.Duration(taskTimeoutSeconds) * time.Second,
		WorkerConcurrency: workerConcurrency,
	}, nil
}

// getEnv 把空字符串视为未配置，避免误覆盖默认值。
func getEnv(key, fallback string) string {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	return value
}

// getEnvInt 读取正整数配置；格式错误时直接让进程启动失败。
func getEnvInt(key string, fallback int) (int, error) {
	value := os.Getenv(key)
	if value == "" {
		return fallback, nil
	}

	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("parse %s: %w", key, err)
	}
	if parsed <= 0 {
		return 0, fmt.Errorf("%s must be greater than 0", key)
	}

	return parsed, nil
}
