from fastapi import FastAPI, HTTPException
from fastapi.responses import StreamingResponse

from demo_agents import AGENTS
from demo_agents.common import InvokeRequest, emit_events


app = FastAPI(title="RelayFlow FastAPI Agent Demo")


# healthz 用于容器健康检查，只确认 FastAPI 进程已经可接收请求。
@app.get("/healthz")
async def healthz() -> dict[str, str]:
    return {"status": "ok"}


# list_agents 返回当前 Demo 服务内置的 Agent 类型，方便 Apifox 或 Worker 选择测试目标。
@app.get("/agents")
async def list_agents() -> dict[str, list[str]]:
    return {"agent_types": sorted(AGENTS.keys())}


# invoke 保留非流式调用入口，Worker 不需要阶段事件时可以直接拿最终结果。
@app.post("/invoke")
async def invoke(request: InvokeRequest):
    if request.agent_type not in AGENTS:
        raise HTTPException(status_code=404, detail=f"unsupported agent_type: {request.agent_type}")
    agent = AGENTS[request.agent_type]
    return await agent.invoke(request)


# invoke_events 返回 SSE 阶段事件流，主线只输出语义事件，不输出 token 逐字流。
@app.post("/invoke/events")
async def invoke_events(request: InvokeRequest) -> StreamingResponse:
    if request.agent_type not in AGENTS:
        raise HTTPException(status_code=404, detail=f"unsupported agent_type: {request.agent_type}")
    agent = AGENTS[request.agent_type]
    return StreamingResponse(emit_events(agent.invoke_events(request)), media_type="text/event-stream")


# invoke_agent 是按路径选择 Agent 的别名，便于把三个 Agent 当成三个独立服务接口测试。
@app.post("/{agent_type}/invoke")
async def invoke_agent(agent_type: str, request: InvokeRequest):
    request = request.model_copy(update={"agent_type": agent_type})
    return await invoke(request)


# invoke_agent_events 是按路径选择 Agent 的 SSE 入口，Apifox 可分别调用 /openai、/claude、/langgraph。
@app.post("/{agent_type}/invoke/events")
async def invoke_agent_events(agent_type: str, request: InvokeRequest) -> StreamingResponse:
    request = request.model_copy(update={"agent_type": agent_type})
    return await invoke_events(request)
