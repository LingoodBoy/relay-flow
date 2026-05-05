# FastAPI Agent Demo

## 启动

```bash
docker compose -f docker-compose.agent.yml up -d --build
```

服务地址：

```text
http://127.0.0.1:8000
```

## 环境变量

```env
OPENAI_BASE_URL=https://api.gemai.cc/v1
OPENAI_MODEL=gpt-5.5
OPENAI_API_KEY=

ANTHROPIC_BASE_URL=https://hone.vvvv.ee
ANTHROPIC_MODEL=claude-sonnet-4-6
ANTHROPIC_API_KEY=
```

## 接口

```http
GET /agents
POST /openai/invoke/events
POST /claude/invoke/events
POST /langgraph/invoke/events
```

请求体：

```json
{
  "input": {
    "prompt": "测试 Agent 阶段事件"
  }
}
```
