from collections.abc import AsyncIterator
from typing import Any, TypedDict

from .common import (
    InvokeRequest,
    call_openai_compatible_chat,
    call_openai_compatible_llm,
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


# invoke 复用普通 OpenAI 兼容调用，保持三个 Demo Agent 的非流式接口一致。
async def invoke(request: InvokeRequest) -> dict[str, Any]:
    return await call_openai_compatible_llm(request)


# plan_node 模拟 Agent 规划阶段，LangGraph 会把该节点输出作为一次图更新事件。
def plan_node(state: GraphState) -> dict[str, str]:
    return {"result": "准备调用天气工具"}


# tool_node 模拟工具执行阶段，用真实 LangGraph 节点表达 tool_use/tool_result 的位置。
def tool_node(state: GraphState) -> dict[str, str]:
    return {"tool_result": format_weather(fake_weather(state["location"]))}


# final_node 调用真实 LLM 汇总工具结果，证明 LangGraph 图里有模型推理节点。
async def final_node(state: GraphState) -> dict[str, str]:
    result = await call_openai_compatible_chat(
        [
            {
                "role": "system",
                "content": "你是 RelayFlow LangGraph Agent，请基于工具结果用一句中文回答。",
            },
            {
                "role": "user",
                "content": f"用户问题：{state['question']}\n工具结果：{state['tool_result']}",
            },
        ]
    )
    return {"result": result}


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
            event_type = "tool_result"
            message = "天气工具返回结果"
            payload["message"] = message
            payload["tool_name"] = "get_weather"
            payload["tool_output"] = fake_weather(initial_state["location"])
        yield event_type, payload

    yield "succeeded", {
        "message": "请求处理完成",
        "result": final_result,
    }
