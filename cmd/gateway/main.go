package main

import (
	"fmt"
	"log"

	"relay-flow/internal/config"
)

// Gateway 负责接收外部 HTTP/SSE 请求，并把任务投递到后端队列。
func main() {
	// 统一从环境变量加载配置，便于本地、Docker 和生产环境复用同一份代码。
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	// Phase 0 先验证进程启动和配置读取，后续再接入 HTTP Server、Redis 和 RabbitMQ。
	fmt.Printf("Gateway started with RabbitMQ=%s Redis=%s Agent=%s TaskTimeout=%s\n",
		cfg.RabbitMQURL,
		cfg.RedisAddr,
		cfg.AgentURL,
		cfg.TaskTimeout,
	)
}
