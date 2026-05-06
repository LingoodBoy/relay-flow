package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"

	"relay-flow/internal/event"
)

const (
	StatusQueued    = "queued"
	StatusRunning   = "running"
	StatusSucceeded = "succeeded"
	StatusFailed    = "failed"
)

var ErrRunNotFound = redis.Nil

// RunRecord 是 Redis 中一次 Run 的基础状态快照。
// Phase 1 只保存 queued；后续会通过事件流继续推进 running/succeeded/failed。
type RunRecord struct {
	RunID     string
	AgentID   string
	Input     json.RawMessage
	Cacheable bool
	CreatedAt time.Time
}

type RunDetail struct {
	RunID     string           `json:"run_id"`
	AgentID   string           `json:"agent_id"`
	Input     json.RawMessage  `json:"input"`
	Cacheable bool             `json:"cacheable"`
	Status    string           `json:"status"`
	CreatedAt time.Time        `json:"created_at"`
	Result    *json.RawMessage `json:"result,omitempty"`
	Error     *json.RawMessage `json:"error,omitempty"`
	Events    []event.RunEvent `json:"events,omitempty"`
}

type RedisStore struct {
	client *redis.Client
}

// NewRedisStore 创建 Redis 状态存储客户端。
func NewRedisStore(redisAddr string) *RedisStore {
	return &RedisStore{
		client: redis.NewClient(&redis.Options{Addr: redisAddr}),
	}
}

// Close 关闭 Redis 客户端持有的连接资源。
func (s *RedisStore) Close() error {
	return s.client.Close()
}

// Ping 检查 Redis 当前是否可访问。
func (s *RedisStore) Ping(ctx context.Context) error {
	return s.client.Ping(ctx).Err()
}

// CreateRunQueued 写入一次 Run 的初始 queued 状态。
func (s *RedisStore) CreateRunQueued(ctx context.Context, run RunRecord) error {
	key := fmt.Sprintf("run:%s", run.RunID)

	// Phase 1 只写最小状态；后续事件模型会继续补 result/error/events。
	values := map[string]any{
		"run_id":     run.RunID,
		"agent_id":   run.AgentID,
		"input":      string(run.Input),
		"cacheable":  run.Cacheable,
		"status":     StatusQueued,
		"created_at": run.CreatedAt.Format(time.RFC3339Nano),
	}

	if err := s.client.HSet(ctx, key, values).Err(); err != nil {
		return fmt.Errorf("write queued run: %w", err)
	}
	return nil
}

// GetRunDetail 查询 Run 当前状态，并按需返回最近 N 条事件。
func (s *RedisStore) GetRunDetail(ctx context.Context, runID string, eventsLimit int64) (RunDetail, error) {
	runKey := fmt.Sprintf("run:%s", runID)

	values, err := s.client.HGetAll(ctx, runKey).Result()
	if err != nil {
		return RunDetail{}, fmt.Errorf("read run detail: %w", err)
	}
	if len(values) == 0 {
		return RunDetail{}, redis.Nil
	}

	detail, err := parseRunDetail(values)
	if err != nil {
		return RunDetail{}, err
	}

	if detail.Status == StatusSucceeded {
		result, err := s.readRawJSON(ctx, fmt.Sprintf("run:%s:result", runID))
		if err != nil {
			return RunDetail{}, err
		}
		detail.Result = result
	}
	if detail.Status == StatusFailed {
		runErr, err := s.readRawJSON(ctx, fmt.Sprintf("run:%s:error", runID))
		if err != nil {
			return RunDetail{}, err
		}
		detail.Error = runErr
	}

	if eventsLimit > 0 {
		events, err := s.listRunEvents(ctx, runID, -eventsLimit, -1)
		if err != nil {
			return RunDetail{}, err
		}
		detail.Events = events
	}

	return detail, nil
}

// parseRunDetail 把 Redis Hash 字段转换成 HTTP 查询模型。
func parseRunDetail(values map[string]string) (RunDetail, error) {
	cacheable, err := strconv.ParseBool(values["cacheable"])
	if err != nil {
		return RunDetail{}, fmt.Errorf("parse run cacheable: %w", err)
	}

	createdAt, err := time.Parse(time.RFC3339Nano, values["created_at"])
	if err != nil {
		return RunDetail{}, fmt.Errorf("parse run created_at: %w", err)
	}

	return RunDetail{
		RunID:     values["run_id"],
		AgentID:   values["agent_id"],
		Input:     json.RawMessage(values["input"]),
		Cacheable: cacheable,
		Status:    values["status"],
		CreatedAt: createdAt,
	}, nil
}

// readRawJSON 读取 Redis 中保存的 JSON 字符串。
func (s *RedisStore) readRawJSON(ctx context.Context, key string) (*json.RawMessage, error) {
	value, err := s.client.Get(ctx, key).Result()
	if errors.Is(err, redis.Nil) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", key, err)
	}

	raw := json.RawMessage(value)
	return &raw, nil
}

// ListRunEvents 读取 Run 已经持久化的全部事件。
// SSE 建连时会先补发这些历史事件，再继续等待实时事件。
func (s *RedisStore) ListRunEvents(ctx context.Context, runID string) ([]event.RunEvent, error) {
	return s.listRunEvents(ctx, runID, 0, -1)
}

// listRunEvents 按 Redis List 范围读取 Run 事件。
func (s *RedisStore) listRunEvents(ctx context.Context, runID string, start int64, stop int64) ([]event.RunEvent, error) {
	key := fmt.Sprintf("run:%s:events", runID)
	values, err := s.client.LRange(ctx, key, start, stop).Result()
	if err != nil {
		return nil, fmt.Errorf("read run events: %w", err)
	}

	events := make([]event.RunEvent, 0, len(values))
	for _, value := range values {
		var evt event.RunEvent
		if err := json.Unmarshal([]byte(value), &evt); err != nil {
			return nil, fmt.Errorf("decode run event: %w", err)
		}
		events = append(events, evt)
	}
	return events, nil
}

// AppendRunEvent 追加 Run 事件，并按事件类型更新当前状态和结果字段。
func (s *RedisStore) AppendRunEvent(ctx context.Context, evt event.RunEvent) error {
	eventKey := fmt.Sprintf("run:%s:events", evt.RunID)
	runKey := fmt.Sprintf("run:%s", evt.RunID)

	body, err := json.Marshal(evt)
	if err != nil {
		return fmt.Errorf("marshal run event: %w", err)
	}

	pipe := s.client.TxPipeline()
	pipe.RPush(ctx, eventKey, body)

	switch evt.Type {
	case event.EventTypeRunning:
		pipe.HSet(ctx, runKey, "status", StatusRunning)
	case event.EventTypeSucceeded:
		pipe.HSet(ctx, runKey, "status", StatusSucceeded)
		pipe.Set(ctx, fmt.Sprintf("run:%s:result", evt.RunID), string(evt.Payload), 0)
	case event.EventTypeFailed:
		pipe.HSet(ctx, runKey, "status", StatusFailed)
		pipe.Set(ctx, fmt.Sprintf("run:%s:error", evt.RunID), string(evt.Payload), 0)
	}

	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("append run event: %w", err)
	}
	return nil
}
