import json
import os
from typing import Any

import httpx
from fastapi import FastAPI, HTTPException
from fastapi.responses import StreamingResponse
from pydantic import BaseModel, Field


OPENAI_BASE_URL = (
    os.getenv("OPENAI_BASE_URL")
    or os.getenv("GEMAI_BASE_URL")
    or os.getenv("GEMINI_API_BASE_URL")
    or "https://api.gemai.cc/v1"
)
OPENAI_MODEL = os.getenv("OPENAI_MODEL") or os.getenv("GEMAI_MODEL") or "gpt-5.5"
OPENAI_API_KEY = os.getenv("OPENAI_API_KEY") or os.getenv("GEMAI_API_KEY") or os.getenv("GEMINI_API_KEY")

app = FastAPI(title="RelayFlow FastAPI Agent Demo")


class InvokeRequest(BaseModel):
    run_id: str = Field(..., min_length=1)
    input: str | dict[str, Any] | list[Any]


def build_messages(user_input: str | dict[str, Any] | list[Any]) -> list[dict[str, str]]:
    # Demo Agent 只负责把 RelayFlow 传入的 input 转成 LLM 消息。
    # 如果 input 是结构化数据，先序列化成 JSON，避免 Python dict 的字符串表示影响模型理解。
    if isinstance(user_input, str):
        content = user_input
    else:
        content = json.dumps(user_input, ensure_ascii=False)

    return [
        {"role": "system", "content": "你是 RelayFlow 示例 Agent，请用简洁中文回答。"},
        {"role": "user", "content": content},
    ]


def require_api_key() -> str:
    # API key 不写进代码仓库，避免示例服务被提交后泄露真实凭据。
    if not OPENAI_API_KEY:
        raise HTTPException(status_code=500, detail="OPENAI_API_KEY is not configured")
    return OPENAI_API_KEY


@app.get("/healthz")
async def healthz() -> dict[str, str]:
    return {"status": "ok"}


@app.post("/invoke")
async def invoke(request: InvokeRequest) -> dict[str, Any]:
    # 非流式接口用于 Worker 的最小闭环：提交 input，等待 LLM 完整返回。
    api_key = require_api_key()
    payload = {
        "model": OPENAI_MODEL,
        "messages": build_messages(request.input),
        "stream": False,
    }

    async with httpx.AsyncClient(timeout=60) as client:
        try:
            response = await client.post(
                f"{OPENAI_BASE_URL}/chat/completions",
                headers={"Authorization": f"Bearer {api_key}"},
                json=payload,
            )
        except httpx.HTTPError as exc:
            raise HTTPException(status_code=502, detail=f"LLM upstream request failed: {exc}") from exc

    if response.status_code >= 400:
        raise HTTPException(status_code=response.status_code, detail=response.text)

    data = response.json()
    content = data["choices"][0]["message"]["content"]
    return {"run_id": request.run_id, "result": content, "raw": data}


@app.post("/invoke/stream")
async def invoke_stream(request: InvokeRequest) -> StreamingResponse:
    # SSE 接口直接透传 OpenAI-compatible 的流式增量，便于前端或 Gateway 验证长连接行为。
    api_key = require_api_key()
    payload = {
        "model": OPENAI_MODEL,
        "messages": build_messages(request.input),
        "stream": True,
    }

    async def event_generator():
        async with httpx.AsyncClient(timeout=None) as client:
            async with client.stream(
                "POST",
                f"{OPENAI_BASE_URL}/chat/completions",
                headers={"Authorization": f"Bearer {api_key}"},
                json=payload,
            ) as response:
                if response.status_code >= 400:
                    error_text = await response.aread()
                    yield f"event: error\ndata: {error_text.decode('utf-8')}\n\n"
                    return

                async for line in response.aiter_lines():
                    if not line.startswith("data: "):
                        continue

                    chunk = line.removeprefix("data: ").strip()
                    if chunk == "[DONE]":
                        yield "event: done\ndata: {}\n\n"
                        return

                    yield f"event: token\ndata: {chunk}\n\n"

    return StreamingResponse(event_generator(), media_type="text/event-stream")
