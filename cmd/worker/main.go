package main

import (
	"fmt"
	"log"

	"relay-flow/internal/config"
)

// Worker 负责消费队列任务、调用 Agent 服务，并回传执行状态。
func main() {
	// 和 Gateway 复用同一套基础配置，避免两类进程对外部依赖的理解不一致。
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	// Phase 0 先验证进程启动和配置读取，后续再接入 RabbitMQ consumer 和 Agent client。
	fmt.Printf("Worker started with RabbitMQ=%s Redis=%s Agent=%s TaskTimeout=%s\n",
		cfg.RabbitMQURL,
		cfg.RedisAddr,
		cfg.AgentURL,
		cfg.TaskTimeout,
	)
}
