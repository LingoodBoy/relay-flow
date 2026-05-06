package worker

import (
	"encoding/json"
	"testing"

	"relay-flow/internal/event"
)

// TestEventAdapterAdaptAcceptedEvent 验证 Agent 阶段事件会被补齐成 RelayFlow 标准事件。
func TestEventAdapterAdaptAcceptedEvent(t *testing.T) {
	adapter := NewEventAdapter("run-001", 2)

	evt, ok, err := adapter.Adapt(AgentRawEvent{
		Type: "tool_use",
		Data: json.RawMessage(`{"message":"调用天气工具","tool_name":"get_weather"}`),
	})
	if err != nil {
		t.Fatalf("adapt event: %v", err)
	}
	if !ok {
		t.Fatalf("expected event to be accepted")
	}
	if evt.RunID != "run-001" {
		t.Fatalf("run id = %q, want run-001", evt.RunID)
	}
	if evt.Seq != 2 {
		t.Fatalf("seq = %d, want 2", evt.Seq)
	}
	if evt.Type != event.EventTypeToolUse {
		t.Fatalf("type = %q, want %q", evt.Type, event.EventTypeToolUse)
	}
	if evt.Message != "调用天气工具" {
		t.Fatalf("message = %q, want 调用天气工具", evt.Message)
	}
	if evt.CreatedAt.IsZero() {
		t.Fatalf("created_at should be set")
	}
	if adapter.NextSeq() != 3 {
		t.Fatalf("next seq = %d, want 3", adapter.NextSeq())
	}
}

// TestEventAdapterAdaptIgnoredDelta 验证 token/text delta 不进入 RelayFlow 主线事件。
func TestEventAdapterAdaptIgnoredDelta(t *testing.T) {
	adapter := NewEventAdapter("run-001", 2)

	_, ok, err := adapter.Adapt(AgentRawEvent{
		Type: "text_delta",
		Data: json.RawMessage(`{"delta":"你"}`),
	})
	if err != nil {
		t.Fatalf("adapt delta: %v", err)
	}
	if ok {
		t.Fatalf("expected text_delta to be ignored")
	}
	if adapter.NextSeq() != 2 {
		t.Fatalf("next seq = %d, want 2", adapter.NextSeq())
	}
}

// TestEventAdapterAdaptInvalidPayload 验证非 JSON payload 不会被写入标准事件。
func TestEventAdapterAdaptInvalidPayload(t *testing.T) {
	adapter := NewEventAdapter("run-001", 2)

	_, _, err := adapter.Adapt(AgentRawEvent{
		Type: "progress",
		Data: json.RawMessage(`not-json`),
	})
	if err == nil {
		t.Fatalf("expected invalid payload error")
	}
}
