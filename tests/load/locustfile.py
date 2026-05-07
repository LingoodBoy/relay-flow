import json
import os
import time
from typing import Iterator

from locust import HttpUser, events, task
from locust.exception import StopUser
from requests import RequestException


AGENT_ID = os.getenv("AGENT_ID", "langgraph")
PROMPT = os.getenv("PROMPT", "帮我查一下北京今天的天气")
SSE_TIMEOUT_SECONDS = float(os.getenv("SSE_TIMEOUT_SECONDS", "600"))


# build_run_payload 生成压测时提交给 Gateway 的任务请求体。
def build_run_payload() -> dict:
    return {
        "agent_id": AGENT_ID,
        "input": {
            "prompt": PROMPT,
        },
    }


# iter_sse_events 按 SSE 空行分隔读取事件块。
def iter_sse_events(lines: Iterator[bytes]) -> Iterator[tuple[str, str]]:
    event_type = "message"
    data_lines: list[str] = []

    for raw_line in lines:
        line = raw_line.decode("utf-8").strip()
        if line == "":
            if data_lines:
                yield event_type, "\n".join(data_lines)
            event_type = "message"
            data_lines = []
            continue
        if line.startswith("event:"):
            event_type = line.removeprefix("event:").strip()
            continue
        if line.startswith("data:"):
            data_lines.append(line.removeprefix("data:").strip())


class SSEUser(HttpUser):
    wait_time = lambda self: 0

    # submit_and_subscribe 只执行一次完整链路，避免 Locust 用户完成后继续循环提交新任务。
    # 该任务会一直等待当前 run 的 SSE 终态，结束后抛出 StopUser 让当前虚拟用户退出。
    @task
    def submit_and_subscribe(self) -> None:
        try:
            create_resp = self.client.post(
                "/v1/runs",
                json=build_run_payload(),
                name="POST /v1/runs",
            )
            if create_resp.status_code != 202:
                return

            try:
                run_id = create_resp.json()["run_id"]
            except (KeyError, json.JSONDecodeError):
                return

            event_count = 0
            terminal_seen = False
            started_at = time.perf_counter()

            try:
                with self.client.get(
                    f"/v1/runs/{run_id}/events",
                    name="GET /v1/runs/:run_id/events",
                    headers={"Accept": "text/event-stream"},
                    stream=True,
                    timeout=SSE_TIMEOUT_SECONDS,
                    catch_response=True,
                ) as response:
                    if response.status_code != 200:
                        response.failure(f"unexpected status {response.status_code}")
                        return

                    for event_type, data in iter_sse_events(response.iter_lines()):
                        event_count += 1
                        if event_type in ("succeeded", "failed"):
                            terminal_seen = True
                            break

                    if terminal_seen:
                        response.success()
                    else:
                        response.failure("sse ended without terminal event")
            except RequestException as exc:
                events.request.fire(
                    request_type="SSE",
                    name="GET /v1/runs/:run_id/events",
                    response_time=(time.perf_counter() - started_at) * 1000,
                    response_length=event_count,
                    exception=exc,
                    context={},
                )
                return

            events.request.fire(
                request_type="SSE",
                name="events received",
                response_time=(time.perf_counter() - started_at) * 1000,
                response_length=event_count,
                exception=None if terminal_seen else RuntimeError("sse ended without terminal event"),
                context={},
            )
        finally:
            raise StopUser()
