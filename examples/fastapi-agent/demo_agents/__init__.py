from . import claude_agent, langgraph_agent, openai_agent


AGENTS = {
    "openai": openai_agent,
    "claude": claude_agent,
    "langgraph": langgraph_agent,
}
