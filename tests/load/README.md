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
locust -f tests/load/locustfile.py --host http://127.0.0.1:8080 --web-host 127.0.0.1
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
  --web-host 127.0.0.1 \
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

## Go SSEBench

`tools/ssebench` 是针对 RelayFlow 的专用 Go 压测器。它不模拟复杂用户行为，只执行一次完整链路：

```text
POST /v1/runs
GET /v1/runs/{run_id}/events
等待 succeeded / failed / dead_letter
```

启动 15000 个一次性用户：

```bash
go run ./tools/ssebench \
  -host http://127.0.0.1:8080 \
  -users 15000 \
  -spawn-rate 500 \
  -timeout 30m
```

输出会包含类似 Locust 的统计表：

```text
Type    Name                          # Requests  # Fails  Median (ms)  95%ile (ms)  99%ile (ms)
POST    POST /v1/runs                 ...
GET     GET /v1/runs/:run_id/events   ...
SSE     events received               ...
```

压测过程中会在进度行显示当前最多的失败原因，结束后会输出 `Failures` 聚合表，用于定位连接失败、非预期状态码和 SSE 异常结束等问题。
