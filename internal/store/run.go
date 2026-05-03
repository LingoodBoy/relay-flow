package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

const StatusQueued = "queued"

type RunRecord struct {
	RunID     string
	AgentID   string
	Input     json.RawMessage
	Cacheable bool
	CreatedAt time.Time
}

type RedisStore struct {
	client *redis.Client
}

func NewRedisStore(redisAddr string) *RedisStore {
	return &RedisStore{
		client: redis.NewClient(&redis.Options{Addr: redisAddr}),
	}
}

func (s *RedisStore) Close() error {
	return s.client.Close()
}

func (s *RedisStore) Ping(ctx context.Context) error {
	return s.client.Ping(ctx).Err()
}

func (s *RedisStore) CreateRunQueued(ctx context.Context, run RunRecord) error {
	key := fmt.Sprintf("run:%s", run.RunID)

	// Phase 1 只写入最小状态。后续事件模型接入后，result/error/events 会继续写到相关 key。
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
