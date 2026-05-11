# RelayFlow

RelayFlow 是一个面向长耗时 Agent 任务的可靠异步中继层，用来把低并发、长耗时、稳定性不可控的 Agent 服务包装成可排队、可重试、可查询、可观测的异步任务流。

它的核心目标是把前端高并发连接压力和后端 Agent 执行压力解耦：Gateway 负责接收 HTTP/SSE 请求并快速入队，Worker 按配置的并发度消费任务并调用 Agent，从而让不可控的突发流量变成可控的后台执行。

## Features

- 异步任务提交：`POST /v1/runs` 快速返回 `run_id`
- SSE 进度推送：`GET /v1/runs/{run_id}/events` 订阅任务阶段事件
- RabbitMQ 削峰：任务入队后由 Worker 按并发度消费
- Worker 并发控制：保护低吞吐 Agent 服务不被突发流量打爆
- Redis 状态存储：保存任务状态、结果和事件历史
- 失败重试与 DLQ：支持 timeout、可重试错误、死信队列
- Agent 事件标准化：将不同 Agent 的阶段事件统一成 RelayFlow RunEvent
- Prometheus / OpenTelemetry：支持指标采集和异步链路追踪

## Architecture

<img src="pics/architecture.png" alt="RelayFlow architecture" width="820">

## Tech Stack

- Go, Gin
- RabbitMQ
- Redis
- Prometheus
- OpenTelemetry, Jaeger
- FastAPI demo Agent
- Docker Compose

## Quick Start

启动基础依赖：

```bash
docker compose -f docker-compose.infra.yml up -d
```

启动示例 Agent：

```bash
docker compose -f docker-compose.agent.yml up -d --build
```

启动 Gateway 和 Worker：

```bash
docker compose -f docker-compose.relay.yml up -d --build
```

## API Usage

创建异步任务：

```bash
curl -X POST http://127.0.0.1:8080/v1/runs \
  -H 'Content-Type: application/json' \
  -d '{
    "agent_id": "langgraph",
    "input": {
      "prompt": "帮我查一下北京今天的天气"
    }
  }'
```

响应示例：

```json
{
  "run_id": "run_xxx",
  "status": "queued"
}
```

查询任务状态：

```bash
curl http://127.0.0.1:8080/v1/runs/{run_id}
```

订阅任务事件：

```bash
curl -N http://127.0.0.1:8080/v1/runs/{run_id}/events
```

SSE 事件示例：

```text
event: running
data: {"run_id":"run_xxx","seq":1,"type":"running","message":"任务开始执行"}

event: succeeded
data: {"run_id":"run_xxx","seq":5,"type":"succeeded","message":"任务执行成功"}
```

## Load Testing

在 `2C2G` 资源限制下，使用 Go SSE 压测器模拟 `15,500` 个用户同时提交任务并订阅 SSE 进度事件。Worker 并发度设置为 `1000`，用于模拟后端 Agent 最大执行并发。整轮压测在约 `13` 分钟内完成，期间请求无异常，SSE 连接正常释放，未观察到协程或连接泄漏。

### Runtime Memory

Go 内存整体保持稳定，压测过程中没有出现持续上涨或异常 GC 压力。

<img src="pics/memory.png" alt="Go memory" width="760">

### Queue Backlog

RabbitMQ 队列在 Worker 按并发度消费时形成可控堆积，并随着任务执行逐步回落。

<img src="pics/mq.png" alt="RabbitMQ queue" width="760">

### SSE Connections and Completed Runs

SSE 连接数随用户启动逐步上升，任务完成后连接正常释放；完成数稳定增长。

<img src="pics/run_total.png" alt="SSE connections and completed runs" width="760">

### Worker Concurrency

Worker 当前执行任务数稳定维持在设定的 `1000` 并发附近，说明后端 Agent 执行压力被控制在预期范围内。

<img src="pics/workers.png" alt="Worker concurrency" width="760">

## Notes

- RelayFlow 不接管 Agent 的业务逻辑、对话上下文或工具调用，只负责异步任务可靠性层。
- RabbitMQ 用于任务消息和低频阶段事件，不用于 token 级高频流式传输。
