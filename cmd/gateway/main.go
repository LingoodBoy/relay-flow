package main

import "fmt"

// 负责启动对外 HTTP 服务
// POST /v1/runs
// GET /v1/runs/{id}
// GET /v1/runs/{id}/events
// GET /healthz
// GET /metrics

// 读取配置
// 初始化 Redis
// 初始化 RabbitMQ producer
// 启动 Gateway HTTP Server
// 启动 Gateway Event Consumer

func main() {
	fmt.Println("Gateway started")
}
