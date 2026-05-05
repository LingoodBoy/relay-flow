import os
from collections.abc import AsyncIterator
from typing import Any

import httpx
from fastapi import HTTPException

from .common import InvokeRequest, extract_location, extract_prompt_text, fake_weather, format_weather


ANTHROPIC_BASE_URL = os.getenv("ANTHROPIC_BASE_URL") or "https://hone.vvvv.ee"
ANTHROPIC_MODEL = os.getenv("ANTHROPIC_MODEL") or "claude-sonnet-4-6"
ANTHROPIC_API_KEY = os.getenv("ANTHROPIC_API_KEY")


# invoke 使用 Anthropic Messages 格式调用 Claude，支持 hone.vvvv.ee 这类 Anthropic 兼容网关。
async def invoke(request: InvokeRequest) -> dict[str, Any]:
    if not ANTHROPIC_API_KEY:
        raise HTTPException(status_code=500, detail="ANTHROPIC_API_KEY is not configured")

    payload = {
        "model": ANTHROPIC_MODEL,
        "max_tokens": 512,
        "messages": [
            {
                "role": "user",
                "content": extract_prompt_text(request.input),
            }
        ],
    }

    async with httpx.AsyncClient(timeout=60) as client:
        try:
            response = await client.post(
                f"{ANTHROPIC_BASE_URL}/v1/messages",
                headers={
                    "x-api-key": ANTHROPIC_API_KEY,
                    "anthropic-version": "2023-06-01",
                    "content-type": "application/json",
                },
                json=payload,
            )
        except httpx.HTTPError as exc:
            raise HTTPException(status_code=502, detail=f"Anthropic upstream request failed: {exc}") from exc

    if response.status_code >= 400:
        raise HTTPException(status_code=response.status_code, detail=response.text)

    data = response.json()
    text = "".join(block.get("text", "") for block in data.get("content", []) if block.get("type") == "text")
    return {
        "result": text,
    }


# invoke_events 使用 Claude Agent SDK 启动真实 Claude Agent 会话，并输出 SDK 原始阶段消息。
async def invoke_events(request: InvokeRequest) -> AsyncIterator[tuple[str, dict[str, Any]]]:
    try:
        from claude_agent_sdk import ClaudeSDKClient, ClaudeAgentOptions
    except ImportError as exc:
        yield "failed", {
            "message": "claude-agent-sdk is not available in this runtime",
            "error": str(exc),
        }
        return

    options = ClaudeAgentOptions(
        system_prompt="你是 RelayFlow Claude Agent 示例。回答必须简洁，最终结果最多一句话。",
        allowed_tools=["Read", "Grep"],
        model=ANTHROPIC_MODEL,
        env={
            # Claude Agent SDK 底层会拉起 Claude Code，第三方 Anthropic 兼容网关也通过环境变量传入。
            key: value
            for key, value in {
                "ANTHROPIC_API_KEY": ANTHROPIC_API_KEY,
                "ANTHROPIC_BASE_URL": ANTHROPIC_BASE_URL,
            }.items()
            if value
        },
    )

    yield "progress", {
        "message": "开始处理请求",
    }

    user_input = extract_prompt_text(request.input)
    location = extract_location(request.input)
    yield "tool_use", {
        "message": "调用天气工具",
        "tool_name": "get_weather",
        "tool_input": {"location": location},
    }
    weather = fake_weather(location)
    weather_result = format_weather(weather)
    yield "tool_result", {
        "message": "天气工具返回结果",
        "tool_output": weather,
    }

    is_error = False
    final_result = ""
    async with ClaudeSDKClient(options=options) as client:
        await client.query(f"{user_input}\n\n已知工具结果：{weather_result}\n请基于工具结果用一句话回答。")
        async for message in client.receive_response():
            is_error = is_error or bool(getattr(message, "is_error", False)) or bool(getattr(message, "error", None))
            if type(message).__name__ == "ResultMessage":
                final_result = str(getattr(message, "result", "") or "")

    if is_error:
        yield "failed", {
            "message": "请求处理失败，请检查 ANTHROPIC_API_KEY 和 ANTHROPIC_BASE_URL",
        }
        return

    yield "succeeded", {
        "message": "请求处理完成",
        "result": final_result,
    }
