# 项目背景
RelayFlow 是一个面向长耗时 AI 任务的可靠异步中继层，将脆弱的长耗时 Agent服务，转化为可靠、可观测、消息驱动的异步任务流。

它基于生产者-消费者模型，将低并发、长耗时、不稳定的 Agent 服务包装成可排队、可重试、可观测、可查询的异步任务流。前端请求不再直接阻塞等待 Agent 执行完成，而是由 Go Gateway 接收请求、投递任务到 RabbitMQ，再由 Worker 消费任务并调用 Agent 服务。

# 核心能力
- 基于 Go + Gin 的 Gateway API
- RabbitMQ 任务队列与事件交换机
- Worker 消费模型
- 兼容 FastAPI / LangChain / LangGraph 等 Agent 服务
- Redis 幂等控制、结果缓存与任务状态缓存
- 基于 SSE 的任务进度推送
- 超时、重试、死信队列与故障隔离
- Prometheus 指标采集与 OpenTelemetry 链路追踪
- Docker Compose 本地一键部署
- 协程泄露

# 架构
```
Frontend
  | POST /v1/runs
  | GET /v1/runs/{id}
  | GET /v1/runs/{id}/events
  v
Gateway API
  | publish task message
  v
RabbitMQ task exchange
  v
Worker
  | POST /invoke
  v
FastAPI Agent

Worker
  | publish run event/result
  v
RabbitMQ event exchange
  v
Gateway Event Consumer
  | 1. 写 Redis run status/result
  | 2. 如果前端在线，推 SSE
  v
Frontend
```
# 项目亮点

- 针对 FastAPI / LangGraph / LangChain 等 Agent 服务长耗时、低并发、连接占用时间长的问题，设计 Gateway + Worker 异步执行架构。由 Go Gateway 负责高并发 HTTP/SSE 连接接入，Worker 负责异步调用下游 Agent 服务，实现接入层与执行层解耦。实测在 2 核 2G 单机环境下可维持 1 万级 SSE 长连接，连接建立成功率 99%+，Gateway 进程内存占用控制在约 500MB 以内。

- 针对 Agent 执行耗时长、吞吐不稳定的问题，引入 RabbitMQ 构建生产者-消费者模型。Gateway 将用户请求转化为异步任务投递到任务队列，Worker 按自身处理能力消费任务并调用 Agent，实现削峰填谷和慢消费。压测中在 Agent 平均响应 5s 的情况下，Gateway 侧请求提交接口 P95 延迟控制在 50ms 以内。

- 针对重复提交、网络重试和热点请求导致的重复计算问题，引入 Redis 实现请求幂等与可选结果缓存。通过 `Idempotency-Key` 避免同一请求被重复创建任务；对明确标记为可缓存的无副作用请求，基于请求摘要缓存结果。在热点请求场景下，Redis 缓存命中后可直接返回历史结果，避免重复调用 Agent。

- 针对任务执行失败、Agent 超时和 Worker 异常退出等场景，设计 RabbitMQ 手动 ACK、失败重试、延迟重试和死信队列机制，避免任务因进程异常而静默丢失，并支持追踪失败原因和最终状态。通过控制最大重试次数和退避时间，防止异常 Agent 服务拖垮 Worker。

- 针对 SSE 长连接可能导致的协程泄露和资源泄露问题，设计连接生命周期治理机制。SSE Handler 基于 `request.Context()` 感知客户端断开，主动释放 RabbitMQ consumer、临时队列、channel 和 heartbeat ticker；同时通过 Prometheus 采集 `go_goroutines`、`relayflow_sse_connections`、`relayflow_sse_disconnect_total` 等指标。压测随机断开 1 万个 SSE 连接后，goroutine 数量可在短时间内回落至基线水平，验证无明显协程泄露。

- 针对系统可观测性不足的问题，引入 Prometheus + OpenTelemetry，采集任务提交量、任务成功率、失败率、重试次数、DLQ 数量、SSE 连接数、队列堆积长度、Agent 调用耗时等指标，并通过 Trace 串联 Gateway 入队、Worker 消费、Agent 调用和事件推送链路，便于定位慢请求和失败节点。
