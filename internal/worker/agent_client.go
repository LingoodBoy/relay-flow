package worker

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"relay-flow/internal/queue"
)

// ErrAgentFailedEvent 表示 Agent 已经通过 SSE 主动返回 failed 事件。
// 调用方可以据此避免再补发一条重复的 failed RunEvent。
var ErrAgentFailedEvent = errors.New("agent returned failed event")

// AgentRawEvent 表示 Agent SSE 接口吐出的原始阶段事件。
// 这里暂时只保留 event 名称和 data 字符串，具体标准化交给 Worker Event Adapter。
type AgentRawEvent struct {
	Type string
	Data json.RawMessage
}

type AgentClient struct {
	baseURL    string
	httpClient *http.Client
}

// NewAgentClient 创建调用黑盒 Agent 的 HTTP 客户端。
func NewAgentClient(baseURL string, timeout time.Duration) *AgentClient {
	return &AgentClient{
		baseURL: strings.TrimRight(baseURL, "/"),
		httpClient: &http.Client{
			// 这里的超时覆盖完整 HTTP 调用，防止黑盒 Agent 慢响应时 Worker 长时间占住消息。
			Timeout: timeout,
		},
	}
}

// StreamEvents 调用 Agent 的 POST /invoke/events 接口，并逐条读取 SSE 阶段事件。
// Agent 只输出自己的业务事件，不需要知道 RelayFlow 的 run_id、seq 或存储细节。
func (c *AgentClient) StreamEvents(ctx context.Context, task queue.TaskMessage, handle func(AgentRawEvent) error) error {
	body, err := json.Marshal(map[string]any{
		"input": json.RawMessage(task.Input),
	})
	if err != nil {
		return fmt.Errorf("marshal agent event request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/invoke/events", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create agent event request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("call agent invoke events: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= http.StatusBadRequest {
		respBody, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			return fmt.Errorf("read agent event error response: %w", readErr)
		}
		return fmt.Errorf("agent events returned status %d: %s", resp.StatusCode, string(respBody))
	}

	eventType := ""
	var eventData json.RawMessage
	succeeded := false
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if eventType != "" {
				raw := AgentRawEvent{
					Type: eventType,
					Data: eventData,
				}
				if err := handle(raw); err != nil {
					return err
				}
				if eventType == "failed" {
					return fmt.Errorf("%w: %s", ErrAgentFailedEvent, string(eventData))
				}
				if eventType == "succeeded" {
					succeeded = true
					break
				}
			}
			eventType = ""
			eventData = nil
			continue
		}
		if strings.HasPrefix(line, "event:") {
			eventType = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			continue
		}
		if strings.HasPrefix(line, "data:") {
			eventData = json.RawMessage(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read agent event stream: %w", err)
	}
	if !succeeded {
		return fmt.Errorf("agent event stream ended without succeeded event")
	}
	return nil
}
