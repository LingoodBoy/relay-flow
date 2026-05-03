## API 设计

### 创建任务

```http
POST /v1/runs
Idempotency-Key: optional-key
Content-Type: application/json
```

```json
{
  "agent_id": "demo-agent",
  "input": {
    "question": "分析一下 RabbitMQ 的 ACK 机制"
  },
  "cacheable": false
}
```

响应：

```json
{
  "run_id": "run_123",
  "status": "queued",
  "cached": false
}
```

### 查询任务

```http
GET /v1/runs/{run_id}
```

响应：

```json
{
  "run_id": "run_123",
  "status": "succeeded",
  "result": {
    "answer": "..."
  },
  "events": [
    {
      "seq": 1,
      "type": "running",
      "message": "任务开始执行"
    },
    {
      "seq": 2,
      "type": "tool_use",
      "message": "使用工具 A 查询数据"
    }
  ]
}
```

### 订阅事件

```http
GET /v1/runs/{run_id}/events
```

SSE 示例：

```text
event: running
data: {"seq":1,"message":"任务开始执行"}

event: tool_use
data: {"seq":2,"message":"使用工具 A 查询数据"}

event: succeeded
data: {"seq":9,"message":"任务执行成功","result":{"answer":"..."}}
```

## RabbitMQ设计
RelayFlow 中 RabbitMQ 分为两类消息：`task message` 和 `event message`。

- `task message` 表示“请执行某个任务”，由 Gateway 发送，由 Worker 消费。
- `event message` 表示“某个任务执行过程中发生了某个事件”，由 Worker 发送，由 Gateway 消费并推送给前端。

### task exchange设计
Gateway 收到 `POST /v1/runs` 后：
Gateway
  -> 生成 run_id
  -> 发布 task message 到 relayflow.task.exchange
  -> RabbitMQ 将消息路由到 relayflow.task.queue
  -> Worker 从 relayflow.task.queue 消费任务
  -> Worker 调用 Agent 服务
队列：
正常任务：
Gateway -> relayflow.task.exchange -> relayflow.task.queue -> Worker


可重试失败：
Worker 调用 Agent 超时 / Agent 返回 5xx
  -> Worker 判断 attempt 未超过 max_attempts
  -> Worker 发布 retrying 事件
  -> Worker 将 attempt + 1 后的 task message 发布到 relayflow.retry.exchange
  -> RabbitMQ 将消息路由到 relayflow.retry.queue
  -> relayflow.retry.queue 等待 TTL
  -> TTL 到期后，消息重新投递到 relayflow.task.exchange
  -> RabbitMQ 再次路由到 relayflow.task.queue
  -> Worker 重新消费任务

超过最大重试次数：
Worker -> relayflow.dlx -> relayflow.dlq

task message：
{
  "run_id": "run_123",
  "agent_id": "demo-agent",
  "attempt": 1,
  "cacheable": false,
  "cache_key": "",
  "trace_id": "trace_abc"
}
### event exchange设计
常见事件：
```
Worker 开始执行
  -> 发布 running 事件

Agent 产生阶段性进度
  -> 发布 progress 事件

Agent 调用工具
  -> 发布 tool_use 事件

Agent 工具调用完成
  -> 发布 tool_result 事件

Worker 执行成功
  -> 发布 succeeded 事件

Worker 执行失败
  -> 发布 failed 事件

任务超时
  -> 发布 timeout 事件

任务进入死信队列
  -> 发布 dead_letter 事件
```

队列：
Worker
  -> 发布 event message 到 relayflow.event.exchange
  -> RabbitMQ 将事件路由到 relayflow.event.persist.queue
  -> Gateway Event Consumer 消费事件并写入 Redis

同时，如果前端正在通过 SSE 订阅该 run：

Worker
  -> 发布 event message 到 relayflow.event.exchange
  -> RabbitMQ 将事件路由到 relayflow.sse.{connection_id}
  -> Gateway SSE Handler 消费事件并推送给前端

event message：
{
  "run_id": "run_123",
  "seq": 3,
  "type": "tool_use",
  "message": "使用工具 A 查询数据",
  "payload": {
    "tool_name": "tool_a"
  },
  "created_at": "2026-05-03T10:00:00Z"
}
