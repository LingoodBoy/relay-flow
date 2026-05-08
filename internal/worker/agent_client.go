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

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"

	"relay-flow/internal/observability"
	"relay-flow/internal/queue"
)

type propagationHeaderCarrier http.Header

// Get 按 OpenTelemetry TextMapCarrier 接口读取 HTTP Header。
func (c propagationHeaderCarrier) Get(key string) string {
	return http.Header(c).Get(key)
}

// Set 按 OpenTelemetry TextMapCarrier 接口写入 HTTP Header。
func (c propagationHeaderCarrier) Set(key string, value string) {
	http.Header(c).Set(key, value)
}

// Keys 返回当前 HTTP Header 中已有的键。
func (c propagationHeaderCarrier) Keys() []string {
	keys := make([]string, 0, len(c))
	for key := range c {
		keys = append(keys, key)
	}
	return keys
}

// ErrAgentFailedEvent 表示 Agent 已经通过 SSE 主动返回 failed 事件。
// 调用方可以据此避免再补发一条重复的 failed RunEvent。
var ErrAgentFailedEvent = errors.New("agent returned failed event")

type AgentStatusError struct {
	StatusCode int
	Body       string
}

// Error 返回上游 Agent HTTP 状态错误，供 Worker 做重试分类。
func (e AgentStatusError) Error() string {
	return fmt.Sprintf("agent events returned status %d: %s", e.StatusCode, e.Body)
}

type AgentInvalidRequestError struct {
	Message string
}

// Error 返回 Agent 请求构造或协议解析错误，这类错误通常不应重试。
func (e AgentInvalidRequestError) Error() string {
	return e.Message
}

// AgentRawEvent 表示 Agent SSE 接口吐出的原始阶段事件。
// 这里暂时只保留 event 名称和 data 字符串，具体标准化交给 Worker Event Adapter。
type AgentRawEvent struct {
	Type string
	Data json.RawMessage
}

type AgentClient struct {
	baseURL    string
	httpClient *http.Client
	timeout    time.Duration
}

// NewAgentClient 创建调用黑盒 Agent 的 HTTP 客户端。
func NewAgentClient(baseURL string, timeout time.Duration) *AgentClient {
	return &AgentClient{
		baseURL: strings.TrimRight(baseURL, "/"),
		httpClient: &http.Client{
			Transport: &http.Transport{
				// Agent 事件流是长连接 POST，请求本身持续时间长，复用 idle connection 的收益很低。
				// 压测时 Uvicorn/代理可能先关闭空闲连接，Go 复用到旧连接会报 server closed idle connection。
				DisableKeepAlives: true,
			},
		},
		timeout: timeout,
	}
}

// StreamEvents 调用 Agent 的 POST /invoke/events 接口，并逐条读取 SSE 阶段事件。
// Agent 只输出自己的业务事件，不需要知道 RelayFlow 的 run_id、seq 或存储细节。
func (c *AgentClient) StreamEvents(ctx context.Context, task queue.TaskMessage, handle func(AgentRawEvent) error) error {
	ctx, span := observability.Tracer().Start(ctx, "worker.agent_invoke_events")
	defer span.End()
	span.SetAttributes(
		attribute.String("run_id", task.RunID),
		attribute.String("agent_id", task.AgentID),
		attribute.String("http.url", c.baseURL+"/invoke/events"),
	)

	if c.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.timeout)
		defer cancel()
	}

	body, err := json.Marshal(map[string]any{
		"input":      json.RawMessage(task.Input),
		"agent_type": task.AgentID,
	})
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "marshal agent request")
		return AgentInvalidRequestError{Message: fmt.Sprintf("marshal agent event request: %v", err)}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/invoke/events", bytes.NewReader(body))
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "create agent request")
		return AgentInvalidRequestError{Message: fmt.Sprintf("create agent event request: %v", err)}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	otel.GetTextMapPropagator().Inject(ctx, propagationHeaderCarrier(req.Header))

	resp, err := c.httpClient.Do(req)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "call agent")
		return fmt.Errorf("call agent invoke events: %w", err)
	}
	defer resp.Body.Close()
	span.SetAttributes(attribute.Int("http.status_code", resp.StatusCode))

	if resp.StatusCode >= http.StatusBadRequest {
		respBody, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			span.RecordError(readErr)
			span.SetStatus(codes.Error, "read agent error response")
			return fmt.Errorf("read agent event error response: %w", readErr)
		}
		span.SetStatus(codes.Error, "agent returned error status")
		return AgentStatusError{StatusCode: resp.StatusCode, Body: string(respBody)}
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
					span.RecordError(err)
					span.SetStatus(codes.Error, "handle agent event")
					return err
				}
				if eventType == "failed" {
					span.SetStatus(codes.Error, "agent returned failed event")
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
		span.RecordError(err)
		span.SetStatus(codes.Error, "read agent stream")
		return fmt.Errorf("read agent event stream: %w", err)
	}
	if !succeeded {
		span.SetStatus(codes.Error, "agent stream ended without succeeded")
		return AgentInvalidRequestError{Message: "agent event stream ended without succeeded event"}
	}
	return nil
}
