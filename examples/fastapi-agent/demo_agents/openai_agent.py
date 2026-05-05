import os
import json
import ast
from collections.abc import AsyncIterator
from typing import Any

from .common import OPENAI_API_KEY, OPENAI_BASE_URL, InvokeRequest, call_openai_compatible_llm, normalize_input


# invoke 复用普通 OpenAI 兼容调用，给不需要阶段事件的链路提供最终结果。
async def invoke(request: InvokeRequest) -> dict[str, Any]:
    return await call_openai_compatible_llm(request)


# get_tool_call_input 提取 SDK 工具调用参数，只保留业务可读字段。
def get_tool_call_input(item: Any) -> dict[str, Any]:
    raw_item = getattr(item, "raw_item", None)
    arguments = getattr(raw_item, "arguments", "") or "{}"
    try:
        return json.loads(arguments)
    except json.JSONDecodeError:
        return {"input": arguments}


# get_tool_output 提取 SDK 工具返回内容，并尽量还原成 JSON 对象而不是 Python 字符串。
def get_tool_output(item: Any) -> Any:
    output = getattr(item, "output", "") or ""
    if isinstance(output, dict):
        return output
    if isinstance(output, str):
        try:
            return json.loads(output)
        except json.JSONDecodeError:
            pass
        try:
            parsed = ast.literal_eval(output)
            if isinstance(parsed, dict):
                return parsed
        except (ValueError, SyntaxError):
            pass
    return output


# invoke_events 使用 OpenAI Agents SDK 运行真实 Agent，并把 SDK 事件转成阶段 SSE 事件。
async def invoke_events(request: InvokeRequest) -> AsyncIterator[tuple[str, dict[str, Any]]]:
    try:
        from agents import Agent, AsyncOpenAI, ModelSettings, OpenAIChatCompletionsModel, RunConfig, Runner, function_tool
    except ImportError as exc:
        yield "failed", {
            "message": "openai-agents is not installed",
            "error": str(exc),
        }
        return

    openai_client = AsyncOpenAI(api_key=OPENAI_API_KEY, base_url=OPENAI_BASE_URL)
    model = OpenAIChatCompletionsModel(
        model=os.getenv("OPENAI_AGENTS_MODEL") or os.getenv("OPENAI_MODEL") or "gpt-4o-mini",
        openai_client=openai_client,
    )

    # get_weather 是真实注册进 OpenAI Agent 的工具，用来产生 tool_call 类阶段事件。
    @function_tool
    def get_weather(location: str) -> dict[str, str]:
        return {
            "location": location,
            "condition": "晴",
            "temperature": "24°C",
            "wind": "东北风 2 级",
        }

    agent = Agent(
        name="RelayFlow OpenAI Agent",
        instructions="你必须先调用 get_weather 工具，再用一句中文回答用户。",
        tools=[get_weather],
        model=model,
    )

    yield "progress", {
        "message": "开始处理请求",
    }

    result = Runner.run_streamed(
        agent,
        input=normalize_input(request.input),
        run_config=RunConfig(
            tracing_disabled=True,
            model_settings=ModelSettings(max_tokens=300),
        ),
    )

    try:
        async for event in result.stream_events():
            event_type = getattr(event, "type", "")
            if event_type == "raw_response_event":
                continue

            if event_type == "agent_updated_stream_event":
                yield "progress", {
                    "message": "准备调用工具",
                }
                continue

            if event_type == "run_item_stream_event":
                item = getattr(event, "item", None)
                name = getattr(event, "name", "")
                item_type = getattr(item, "type", "")
                if name == "tool_called" or item_type == "tool_call_item":
                    yield "tool_use", {
                        "message": "调用天气工具",
                        "tool_name": getattr(getattr(item, "raw_item", None), "name", ""),
                        "tool_input": get_tool_call_input(item),
                    }
                    continue
                if name == "tool_output" or item_type == "tool_call_output_item":
                    yield "tool_result", {
                        "message": "天气工具返回结果",
                        "tool_output": get_tool_output(item),
                    }
                    continue
    except Exception as exc:
        yield "failed", {
            "message": "请求处理失败",
            "error": str(exc),
        }
        return

    yield "succeeded", {
        "message": "请求处理完成",
        "result": getattr(result, "final_output", None),
    }
