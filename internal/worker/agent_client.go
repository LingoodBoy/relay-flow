package worker

import (
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

// Invoke 调用 Agent 的 POST /invoke 接口并返回原始响应体。
// run_id 属于 RelayFlow 内部调度 ID，不传给黑盒 Agent，避免 Agent 被迫理解系统追踪字段。
func (c *AgentClient) Invoke(ctx context.Context, task queue.TaskMessage) ([]byte, error) {
	// RelayFlow 透传 input，不关心 Agent 的业务 schema；这样 Gateway/Worker 可以服务不同 Agent。
	body, err := json.Marshal(map[string]any{
		"input": json.RawMessage(task.Input),
	})
	if err != nil {
		return nil, fmt.Errorf("marshal agent request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/invoke", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create agent request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("call agent invoke: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read agent response: %w", err)
	}
	if resp.StatusCode >= http.StatusBadRequest {
		return nil, fmt.Errorf("agent returned status %d: %s", resp.StatusCode, string(respBody))
	}

	return respBody, nil
}
