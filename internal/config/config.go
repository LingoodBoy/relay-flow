package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

const (
	// 默认值面向本地开发；Docker Compose 会用服务名覆盖这些地址。
	defaultRabbitMQURL        = "amqp://guest:guest@localhost:5672/"
	defaultRedisAddr          = "localhost:6379"
	defaultAgentHTTPURL       = "http://localhost:8000"
	defaultGatewayAddr        = ":8080"
	defaultAgentTimeoutSecond = 0
	defaultWorkerConcurrency  = 20
	defaultMaxAttempts        = 3
)

// Config 是 Gateway 和 Worker 共享的基础运行时配置。
type Config struct {
	RabbitMQURL       string
	RedisAddr         string
	AgentURL          string
	GatewayAddr       string
	AgentTimeout      time.Duration
	WorkerConcurrency int
	MaxAttempts       int
}

// Load 从环境变量加载配置；未设置时使用本地开发默认值。
func Load() (Config, error) {
	agentTimeoutSeconds, err := getEnvIntAllowZero("RELAYFLOW_AGENT_TIMEOUT_SECONDS", defaultAgentTimeoutSecond)
	if err != nil {
		return Config{}, err
	}
	workerConcurrency, err := getEnvInt("RELAYFLOW_WORKER_CONCURRENCY", defaultWorkerConcurrency)
	if err != nil {
		return Config{}, err
	}
	maxAttempts, err := getEnvInt("RELAYFLOW_MAX_ATTEMPTS", defaultMaxAttempts)
	if err != nil {
		return Config{}, err
	}

	return Config{
		RabbitMQURL:       getEnv("RELAYFLOW_RABBITMQ_URL", defaultRabbitMQURL),
		RedisAddr:         getEnv("RELAYFLOW_REDIS_ADDR", defaultRedisAddr),
		AgentURL:          getEnv("RELAYFLOW_AGENT_URL", defaultAgentHTTPURL),
		GatewayAddr:       getEnv("RELAYFLOW_GATEWAY_ADDR", defaultGatewayAddr),
		AgentTimeout:      time.Duration(agentTimeoutSeconds) * time.Second,
		WorkerConcurrency: workerConcurrency,
		MaxAttempts:       maxAttempts,
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

// getEnvIntAllowZero 读取非负整数配置；0 通常表示关闭某个可选限制。
func getEnvIntAllowZero(key string, fallback int) (int, error) {
	value := os.Getenv(key)
	if value == "" {
		return fallback, nil
	}

	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("parse %s: %w", key, err)
	}
	if parsed < 0 {
		return 0, fmt.Errorf("%s must be greater than or equal to 0", key)
	}

	return parsed, nil
}
