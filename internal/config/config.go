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
	defaultWorkerMetricsAddr  = ":9091"
	defaultAgentTimeoutSecond = 0
	defaultWorkerConcurrency  = 20
	defaultMaxAttempts        = 3
	defaultLogLevel           = "info"
	defaultLogSampleRate      = 0.01
	defaultSlowRequestMS      = 300000
)

// Config 是 Gateway 和 Worker 共享的基础运行时配置。
type Config struct {
	RabbitMQURL       string
	RedisAddr         string
	AgentURL          string
	GatewayAddr       string
	WorkerMetricsAddr string
	AgentTimeout      time.Duration
	WorkerConcurrency int
	MaxAttempts       int
	LogLevel          string
	LogSampleRate     float64
	SlowRequestMS     int
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
	logSampleRate, err := getEnvFloat01("RELAYFLOW_LOG_SAMPLE_RATE", defaultLogSampleRate)
	if err != nil {
		return Config{}, err
	}
	slowRequestMS, err := getEnvInt("RELAYFLOW_SLOW_REQUEST_MS", defaultSlowRequestMS)
	if err != nil {
		return Config{}, err
	}

	return Config{
		RabbitMQURL:       getEnv("RELAYFLOW_RABBITMQ_URL", defaultRabbitMQURL),
		RedisAddr:         getEnv("RELAYFLOW_REDIS_ADDR", defaultRedisAddr),
		AgentURL:          getEnv("RELAYFLOW_AGENT_URL", defaultAgentHTTPURL),
		GatewayAddr:       getEnv("RELAYFLOW_GATEWAY_ADDR", defaultGatewayAddr),
		WorkerMetricsAddr: getEnv("RELAYFLOW_WORKER_METRICS_ADDR", defaultWorkerMetricsAddr),
		AgentTimeout:      time.Duration(agentTimeoutSeconds) * time.Second,
		WorkerConcurrency: workerConcurrency,
		MaxAttempts:       maxAttempts,
		LogLevel:          getEnv("RELAYFLOW_LOG_LEVEL", defaultLogLevel),
		LogSampleRate:     logSampleRate,
		SlowRequestMS:     slowRequestMS,
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

// getEnvFloat01 读取 0 到 1 之间的浮点数；用于控制正常请求日志采样率。
func getEnvFloat01(key string, fallback float64) (float64, error) {
	value := os.Getenv(key)
	if value == "" {
		return fallback, nil
	}

	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return 0, fmt.Errorf("parse %s: %w", key, err)
	}
	if parsed < 0 || parsed > 1 {
		return 0, fmt.Errorf("%s must be between 0 and 1", key)
	}

	return parsed, nil
}
