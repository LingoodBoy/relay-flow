package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

const StatusQueued = "queued"

// RunRecord 是 Redis 中一次 Run 的基础状态快照。
// Phase 1 只保存 queued；后续会通过事件流继续推进 running/succeeded/failed。
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
