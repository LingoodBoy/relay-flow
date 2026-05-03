## 项目背景

RelayFlow 是一个面向长耗时 AI 任务的可靠异步中继层，旨在将低并发、长耗时、稳定性不可控的 Agent 服务包装为可排队、可重试、可观测、可查询的高可用异步任务流。

传统 Agent 服务通常采用同步阻塞调用模式，前端请求会长时间占用连接并直接压到 Agent 执行层，导致请求并发和 Agent 执行并发强绑定。流量突增时，系统容易出现连接堆积、请求超时和服务雪崩。RelayFlow 基于生产者-消费者模型，由 Go Gateway 承接高并发 HTTP/SSE 请求并快速投递到 RabbitMQ，再由 Worker 按照设定并发度消费任务并调用下游 Agent 服务，从而将不可控并发转化为可控并发，将同步阻塞调用转化为异步任务流，并将 Agent 服务从高并发连接压力中解放出来。

RelayFlow 不接管 Agent 的对话上下文、记忆和业务逻辑。Agent 仍然是黑盒 HTTP 服务，自己负责上下文、工具调用和模型推理。RelayFlow 只负责可靠性层面的能力。

## 核心能力

- 基于 Go + Gin 的 Gateway API
- Gateway + Worker 异步执行架构
- RabbitMQ 任务队列与事件交换机
- 兼容 FastAPI / LangChain / LangGraph 等 HTTP Agent 服务
- Redis 幂等控制、可选结果缓存与短期任务状态存储
- 基于 SSE 的任务进度和阶段事件推送
- Agent 阶段事件标准化与执行过程追溯
- 超时控制、失败重试、死信队列与故障隔离
- Worker 并发控制，将前端高并发转化为 Agent 可承受的执行并发
- SSE 连接生命周期治理与协程泄露防护
- Prometheus 指标采集与 OpenTelemetry 链路追踪
- Docker Compose 本地一键部署

## 执行模式

RelayFlow 以 Reliable Run 模式为主。

- 前端通过 `POST /v1/runs` 创建异步任务
- Gateway 快速返回 `run_id`
- Worker 后台消费任务并调用 Agent
- Worker 将 Agent 的阶段性事件标准化后发布到 RabbitMQ event exchange
- Gateway Event Consumer 消费事件，写入 Redis，并在前端在线时通过 SSE 推送
- 前端可通过 `GET /v1/runs/{id}` 查询最终状态和结果
- 前端可通过 `GET /v1/runs/{id}/events` 订阅任务进度和阶段事件

RelayFlow 不在 MQ 模式中传输 token 级逐字流。RabbitMQ 适合传任务消息和低频、高语义价值的 Agent 阶段事件，不适合传高频 token。对于需要强实时吐字效果的场景，后续预留 Realtime Stream 模式，由 Gateway 直接反向代理 Agent 的流式接口。

## 核心架构

```text
Frontend
  | POST /v1/runs
  | GET /v1/runs/{id}
  | GET /v1/runs/{id}/events
  v
Gateway API
  | 1. Redis 幂等校验 / 缓存查询
  | 2. 创建 run 状态
  | 3. publish task message
  v
RabbitMQ task exchange
  v
Worker
  | 1. 按并发上限消费任务
  | 2. POST /invoke 或 /invoke/events
  v
FastAPI Agent

FastAPI Agent
  | 返回最终结果
  | 或返回阶段事件流
  v
Worker Event Adapter
  | 1. 标准化 Agent 原始事件
  | 2. 聚合低频阶段事件
  | 3. publish run event/result
  v
RabbitMQ event exchange
  |------------------------------|
  v                              v
Gateway Event Consumer           Gateway SSE 临时队列
  |                              |
  | 写 Redis run 状态/结果/事件历史 | 推送在线前端
  v                              v
Redis                            Frontend
```
# 快速开始
docker compose -f docker-compose.infra.yml up -d
docker compose -f docker-compose.agent.yml up -d --build
docker compose -f docker-compose.relay.yml up -d --build

# 停止
docker compose -f docker-compose.relay.yml down
docker compose -f docker-compose.agent.yml down
docker compose -f docker-compose.infra.yml down
