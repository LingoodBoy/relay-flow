# Load Tests

## Locust

安装：

```bash
pip install locust
```

启动 RelayFlow：

```bash
docker compose -f docker-compose.infra.yml up -d
docker compose -f docker-compose.agent.yml up -d --build
docker compose -f docker-compose.relay.yml up -d --build
```

启动 Locust：

```bash
locust -f tests/load/locustfile.py --host http://127.0.0.1:8080
```

打开 Web UI：

```text
http://127.0.0.1:8089
```

脚本只保留 `SSEUser`，每个虚拟用户只执行一次：

```text
POST /v1/runs
GET /v1/runs/{run_id}/events
等待 succeeded 或 failed
当前用户退出，不再循环提交任务
```

命令行压 1000 个一次性用户：

```bash
locust -f tests/load/locustfile.py \
  --host http://127.0.0.1:8080 \
  --headless \
  -u 1000 \
  -r 100
```

常用环境变量：

```bash
AGENT_ID=langgraph
PROMPT=帮我查一下北京今天的天气
SSE_TIMEOUT_SECONDS=600
```
