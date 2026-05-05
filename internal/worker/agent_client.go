package worker

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"relay-flow/internal/queue"
)

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

// InvokeEvents 调用 Agent 的 POST /invoke/events 接口，读取 SSE 并返回最终 succeeded 事件数据。
// 这里只做“能消费事件流”的最小闭环；事件标准化和逐条发布会放到 Worker Event Adapter。
func (c *AgentClient) InvokeEvents(ctx context.Context, task queue.TaskMessage) ([]byte, error) {
	body, err := json.Marshal(map[string]any{
		"input": json.RawMessage(task.Input),
	})
	if err != nil {
		return nil, fmt.Errorf("marshal agent event request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/invoke/events", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create agent event request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("call agent invoke events: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= http.StatusBadRequest {
		respBody, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			return nil, fmt.Errorf("read agent event error response: %w", readErr)
		}
		return nil, fmt.Errorf("agent events returned status %d: %s", resp.StatusCode, string(respBody))
	}

	eventType := ""
	var finalData []byte
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if eventType == "failed" {
				return nil, fmt.Errorf("agent event failed: %s", string(finalData))
			}
			if eventType == "succeeded" {
				return finalData, nil
			}
			eventType = ""
			finalData = nil
			continue
		}
		if strings.HasPrefix(line, "event:") {
			eventType = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			continue
		}
		if strings.HasPrefix(line, "data:") {
			finalData = []byte(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read agent event stream: %w", err)
	}
	return nil, fmt.Errorf("agent event stream ended without succeeded event")
}
