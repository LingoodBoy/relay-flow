import asyncio
import random
from collections.abc import AsyncIterator
from typing import Any, TypedDict

from .common import (
    InvokeRequest,
    extract_location,
    extract_prompt_text,
    fake_weather,
    format_weather,
)


class GraphState(TypedDict):
    question: str
    location: str
    tool_result: str
    result: str


# pause_stage 在 LangGraph 演示里人为放慢阶段切换，方便观察 SSE 推送顺序。
async def pause_stage() -> None:
    await asyncio.sleep(8)


# invoke 复用普通 OpenAI 兼容调用，保持三个 Demo Agent 的非流式接口一致。
async def invoke(request: InvokeRequest) -> dict[str, Any]:
    location = extract_location(request.input)
    await asyncio.sleep(random.uniform(1, 3))
    return {"result": random_weather_answer(location)}


# plan_node 模拟 Agent 规划阶段，LangGraph 会把该节点输出作为一次图更新事件。
def plan_node(state: GraphState) -> dict[str, str]:
    return {"result": "准备调用天气工具"}


# tool_node 模拟工具执行阶段，用真实 LangGraph 节点表达 tool_use/tool_result 的位置。
def tool_node(state: GraphState) -> dict[str, str]:
    return {"tool_result": format_weather(fake_weather(state["location"]))}


# final_node 模拟 LLM 汇总阶段，压测时不调用真实模型，避免产生额外费用。
async def final_node(state: GraphState) -> dict[str, str]:
    await asyncio.sleep(random.uniform(1, 5))
    return {"result": random_weather_answer(state["location"])}


# random_weather_answer 返回一个本地随机结果，保留 Agent 最终回答形态。
def random_weather_answer(location: str) -> str:
    templates = [
        f"{location}今天晴，气温 24°C，东北风 2 级。",
        f"{location}天气不错，晴天，适合出门。",
        f"{location}当前天气为晴，体感舒适。",
    ]
    return random.choice(templates)


# invoke_events 构建并运行一个真实 LangGraph 状态图，将图更新转换为阶段 SSE 事件。
async def invoke_events(request: InvokeRequest) -> AsyncIterator[tuple[str, dict[str, Any]]]:
    try:
        from langgraph.graph import END, START, StateGraph
    except ImportError as exc:
        yield "failed", {
            "message": "langgraph is not installed",
            "error": str(exc),
        }
        return

    graph = StateGraph(GraphState)
    graph.add_node("planner", plan_node)
    graph.add_node("retriever", tool_node)
    graph.add_node("writer", final_node)
    graph.add_edge(START, "planner")
    graph.add_edge("planner", "retriever")
    graph.add_edge("retriever", "writer")
    graph.add_edge("writer", END)
    app = graph.compile()

    yield "progress", {
        "message": "开始处理请求",
    }
    await pause_stage()

    question = extract_prompt_text(request.input)
    initial_state: GraphState = {
        "question": question,
        "location": extract_location(request.input),
        "tool_result": "",
        "result": "",
    }
    final_result = ""
    async for update in app.astream(initial_state, stream_mode="updates"):
        node_name = next(iter(update.keys()), "")
        node_output = update.get(node_name, {})
        if node_name == "writer":
            final_result = node_output.get("result", "")
            continue
        event_type = "progress"
        message = node_output.get("result", "处理阶段更新")
        payload: dict[str, Any] = {
            "message": message,
        }
        if node_name == "retriever":
            yield "tool_use", {
                "message": "调用天气工具",
                "tool_name": "get_weather",
                "tool_input": {"location": initial_state["location"]},
            }
            await pause_stage()
            event_type = "tool_result"
            message = "天气工具返回结果"
            payload["message"] = message
            payload["tool_name"] = "get_weather"
            payload["tool_output"] = fake_weather(initial_state["location"])
        yield event_type, payload
        await pause_stage()

    await pause_stage()
    yield "succeeded", {
        "message": "请求处理完成",
        "result": final_result,
    }
