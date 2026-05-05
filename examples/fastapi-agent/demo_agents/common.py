import json
import os
import re
from collections.abc import AsyncIterator
from typing import Any

import httpx
from fastapi import HTTPException
from pydantic import BaseModel, Field


OPENAI_BASE_URL = (
    os.getenv("OPENAI_BASE_URL")
    or os.getenv("GEMAI_BASE_URL")
    or os.getenv("GEMINI_API_BASE_URL")
    or "https://api.gemai.cc/v1"
)
OPENAI_MODEL = os.getenv("OPENAI_MODEL") or os.getenv("GEMAI_MODEL") or "gpt-5.5"
OPENAI_API_KEY = os.getenv("OPENAI_API_KEY") or os.getenv("GEMAI_API_KEY") or os.getenv("GEMINI_API_KEY")


class InvokeRequest(BaseModel):
    input: str | dict[str, Any] | list[Any]
    agent_type: str = Field(default="openai", pattern="^(openai|claude|langgraph)$")


# normalize_input 把不同形态的 input 统一成字符串，避免各个 Agent 重复处理请求体。
def normalize_input(user_input: str | dict[str, Any] | list[Any]) -> str:
    if isinstance(user_input, str):
        return user_input
    return json.dumps(user_input, ensure_ascii=False)


# extract_prompt_text 从 Demo 请求里提取用户可读文本，避免工具事件里出现整段 JSON 字符串。
def extract_prompt_text(user_input: str | dict[str, Any] | list[Any]) -> str:
    if isinstance(user_input, str):
        return user_input
    if isinstance(user_input, dict):
        prompt = user_input.get("prompt") or user_input.get("input") or user_input.get("query")
        if isinstance(prompt, str):
            return prompt
    return normalize_input(user_input)


# extract_location 从中文天气请求里提取地点；Demo 只做最小规则，真实 Agent 可由模型或工具层解析。
def extract_location(user_input: str | dict[str, Any] | list[Any]) -> str:
    text = extract_prompt_text(user_input)
    match = re.search(r"(北京|上海|广州|深圳|杭州|成都|南京|武汉|西安|重庆|天津)", text)
    if match:
        return match.group(1)
    return "北京"


# fake_weather 返回稳定的天气工具结果，方便测试 Agent 阶段事件链路。
def fake_weather(location: str) -> dict[str, str]:
    return {
        "location": location,
        "condition": "晴",
        "temperature": "24°C",
        "wind": "东北风 2 级",
    }


# format_weather 把工具结果转成一句自然语言，供 LLM 汇总或兜底结果使用。
def format_weather(weather: dict[str, str]) -> str:
    return f"{weather['location']}：{weather['condition']}，气温 {weather['temperature']}，{weather['wind']}。"


# build_messages 生成 OpenAI Chat Completions 格式消息，普通 invoke 走 OpenAI 兼容接口。
def build_messages(user_input: str | dict[str, Any] | list[Any]) -> list[dict[str, str]]:
    return [
        {"role": "system", "content": "你是 RelayFlow 示例 Agent，请用简洁中文回答。"},
        {"role": "user", "content": normalize_input(user_input)},
    ]


# build_llm_messages 生成自定义 system/user 消息，给 LangGraph 等 Agent 节点复用。
def build_llm_messages(system_prompt: str, user_prompt: str) -> list[dict[str, str]]:
    return [
        {"role": "system", "content": system_prompt},
        {"role": "user", "content": user_prompt},
    ]


# require_api_key 在真正调用 LLM 前检查密钥，缺少配置时返回清晰的服务端错误。
def require_api_key() -> str:
    if not OPENAI_API_KEY:
        raise HTTPException(status_code=500, detail="OPENAI_API_KEY is not configured")
    return OPENAI_API_KEY


# call_openai_compatible_chat 使用 OpenAI 兼容协议调用模型，并返回最终文本。
async def call_openai_compatible_chat(messages: list[dict[str, str]]) -> str:
    payload = {
        "model": OPENAI_MODEL,
        "messages": messages,
        "stream": False,
    }

    async with httpx.AsyncClient(timeout=60) as client:
        try:
            response = await client.post(
                f"{OPENAI_BASE_URL}/chat/completions",
                headers={"Authorization": f"Bearer {require_api_key()}"},
                json=payload,
            )
        except httpx.HTTPError as exc:
            raise HTTPException(status_code=502, detail=f"LLM upstream request failed: {exc}") from exc

    if response.status_code >= 400:
        raise HTTPException(status_code=response.status_code, detail=response.text)

    data = response.json()
    return data["choices"][0]["message"]["content"]


# call_openai_compatible_llm 使用 OpenAI 兼容协议调用模型，便于接入 gemai.cc 这类兼容服务。
async def call_openai_compatible_llm(request: InvokeRequest) -> dict[str, Any]:
    return {
        "result": await call_openai_compatible_chat(build_messages(request.input)),
    }


# make_sse 把阶段事件编码成标准 SSE 文本块，Gateway/Apifox 都可以直接消费。
def make_sse(event: str, data: dict[str, Any]) -> str:
    return f"event: {event}\ndata: {json.dumps(data, ensure_ascii=False)}\n\n"


# emit_events 负责把 Agent 的异步事件迭代器转换成 StreamingResponse 可发送的数据流。
async def emit_events(events: AsyncIterator[tuple[str, dict[str, Any]]]) -> AsyncIterator[str]:
    async for event, data in events:
        yield make_sse(event, data)
