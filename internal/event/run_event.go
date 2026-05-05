package event

import (
	"encoding/json"
	"time"
)

// EventType 表示 Run 生命周期中的标准事件类型。
type EventType string

const (
	EventTypeRunning    EventType = "running"
	EventTypeProgress   EventType = "progress"
	EventTypeToolUse    EventType = "tool_use"
	EventTypeToolResult EventType = "tool_result"
	EventTypeSucceeded  EventType = "succeeded"
	EventTypeFailed     EventType = "failed"
	EventTypeTimeout    EventType = "timeout"
	EventTypeDeadLetter EventType = "dead_letter"
)

// RunEvent 是 RelayFlow 内部统一的 Run 事件结构。
// Worker 发布、Gateway 消费、Redis 存储和 SSE 推送都围绕这个结构传递状态变化。
type RunEvent struct {
	RunID     string          `json:"run_id"`
	Seq       int64           `json:"seq"`
	Type      EventType       `json:"type"`
	Message   string          `json:"message"`
	Payload   json.RawMessage `json:"payload,omitempty"`
	CreatedAt time.Time       `json:"created_at"`
}
