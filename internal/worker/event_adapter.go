package worker

import (
	"encoding/json"
	"fmt"
	"time"

	"relay-flow/internal/event"
)

// EventAdapter 把 Agent 原始阶段事件转换成 RelayFlow 标准 RunEvent。
// Agent 不维护 run_id、seq、created_at，这些字段由 Worker 在进入 RelayFlow 事件链路前补齐。
type EventAdapter struct {
	runID string
	seq   int64
}

// NewEventAdapter 创建单个 Run 专用的事件适配器。
// startSeq 表示下一条 Agent 阶段事件要使用的序号，通常接在 Worker 自己发布的 running 事件之后。
func NewEventAdapter(runID string, startSeq int64) *EventAdapter {
	return &EventAdapter{
		runID: runID,
		seq:   startSeq,
	}
}

// NextSeq 返回下一条标准事件会使用的序号。
// 当 Agent 流中途异常时，Worker 可以用它补发 failed 事件，避免和已发布事件撞序号。
func (a *EventAdapter) NextSeq() int64 {
	return a.seq
}

// Adapt 将 AgentRawEvent 转成 RelayFlow RunEvent。
// 第二个返回值表示该事件是否应该进入 RelayFlow；token/text delta 这类逐字流会被过滤。
func (a *EventAdapter) Adapt(raw AgentRawEvent) (event.RunEvent, bool, error) {
	eventType, ok := normalizeAgentEventType(raw.Type)
	if !ok {
		return event.RunEvent{}, false, nil
	}

	payload, err := normalizeAgentEventPayload(raw.Data)
	if err != nil {
		return event.RunEvent{}, false, err
	}

	evt := event.RunEvent{
		RunID:     a.runID,
		Seq:       a.seq,
		Type:      eventType,
		Message:   extractAgentEventMessage(payload, eventType),
		Payload:   payload,
		CreatedAt: time.Now().UTC(),
	}
	a.seq++
	return evt, true, nil
}

// normalizeAgentEventType 只放行 RelayFlow 主线关心的阶段事件。
// Agent SDK 里的 token delta、text delta、message delta 属于逐字流，主线不保存也不推送。
func normalizeAgentEventType(rawType string) (event.EventType, bool) {
	switch rawType {
	case string(event.EventTypeProgress):
		return event.EventTypeProgress, true
	case string(event.EventTypeToolUse):
		return event.EventTypeToolUse, true
	case string(event.EventTypeToolResult):
		return event.EventTypeToolResult, true
	case string(event.EventTypeSucceeded):
		return event.EventTypeSucceeded, true
	case string(event.EventTypeFailed):
		return event.EventTypeFailed, true
	case "delta", "text_delta", "token_delta", "message_delta":
		return "", false
	default:
		return "", false
	}
}

// normalizeAgentEventPayload 校验 Agent data 是否是 JSON 对象。
// 当前标准事件要求 payload 直接保存业务字段，避免把字符串、SDK repr 等冗余内容写入 Redis。
func normalizeAgentEventPayload(data json.RawMessage) (json.RawMessage, error) {
	if len(data) == 0 {
		return nil, nil
	}

	var value map[string]any
	if err := json.Unmarshal(data, &value); err != nil {
		return nil, fmt.Errorf("decode agent event payload: %w", err)
	}

	payload, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("marshal agent event payload: %w", err)
	}
	return payload, nil
}

// extractAgentEventMessage 从 payload.message 提取人类可读的阶段说明。
// 如果 Agent 没给 message，就用事件类型生成一个兜底文案，保证查询接口里始终有可读信息。
func extractAgentEventMessage(payload json.RawMessage, eventType event.EventType) string {
	if len(payload) > 0 {
		var value struct {
			Message string `json:"message"`
		}
		if err := json.Unmarshal(payload, &value); err == nil && value.Message != "" {
			return value.Message
		}
	}

	switch eventType {
	case event.EventTypeProgress:
		return "任务处理中"
	case event.EventTypeToolUse:
		return "调用工具"
	case event.EventTypeToolResult:
		return "工具返回结果"
	case event.EventTypeSucceeded:
		return "任务执行成功"
	case event.EventTypeFailed:
		return "任务执行失败"
	default:
		return "任务状态更新"
	}
}
