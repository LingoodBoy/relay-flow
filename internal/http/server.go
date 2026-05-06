package http

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"relay-flow/internal/event"
	"relay-flow/internal/logger"
	"relay-flow/internal/queue"
	"relay-flow/internal/store"
)

// Server 封装 Gateway 的 HTTP 路由和外部依赖，让 main 只负责进程启动。
type Server struct {
	router    *gin.Engine
	store     RunStore
	publisher TaskPublisher
	sseHub    *SSEHub
}

// RunStore 是 HTTP 层写入 Run 状态所需的最小接口。
type RunStore interface {
	CreateRunQueued(ctx context.Context, run store.RunRecord) error
	GetRunDetail(ctx context.Context, runID string, eventsLimit int64) (store.RunDetail, error)
	ListRunEvents(ctx context.Context, runID string) ([]event.RunEvent, error)
}

// TaskPublisher 是 HTTP 层发布异步任务所需的最小接口。
type TaskPublisher interface {
	PublishTask(ctx context.Context, task queue.TaskMessage) error
}

type Dependencies struct {
	Store     RunStore
	Publisher TaskPublisher
	SSEHub    *SSEHub
}

// NewServer 创建 Gateway HTTP Server。
func NewServer(deps Dependencies) *Server {
	gin.SetMode(gin.ReleaseMode)

	router := gin.New()
	router.Use(logger.GinRequestLogger())
	router.Use(logger.GinRecovery())

	server := &Server{
		router:    router,
		store:     deps.Store,
		publisher: deps.Publisher,
		sseHub:    deps.SSEHub,
	}
	if server.sseHub == nil {
		server.sseHub = NewSSEHub()
	}
	server.registerRoutes()
	return server
}

// Handler 暴露标准库 http.Handler，便于测试和自定义 http.Server。
func (s *Server) Handler() http.Handler {
	return s.router
}

// registerRoutes 集中注册 Gateway 当前暴露的 HTTP 路由。
func (s *Server) registerRoutes() {
	s.router.GET("/healthz", s.handleHealthz)
	s.router.GET("/readyz", s.handleReadyz)
	s.router.POST("/v1/runs", s.handleCreateRun)
	s.router.GET("/v1/runs/:run_id", s.handleGetRun)
	s.router.GET("/v1/runs/:run_id/events", s.handleRunEvents)
}

// handleHealthz 返回进程存活状态，不检查外部依赖。
func (s *Server) handleHealthz(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// handleReadyz 返回服务是否准备好接收流量。
func (s *Server) handleReadyz(c *gin.Context) {
	// readyz 表示 Gateway 已完成自身初始化，可以接收流量。
	// Redis/RabbitMQ 接入后，这里会扩展为真实依赖检查。
	c.JSON(http.StatusOK, gin.H{"status": "ready"})
}

type createRunRequest struct {
	AgentID   string          `json:"agent_id"`
	Input     json.RawMessage `json:"input"`
	Cacheable bool            `json:"cacheable"`
}

// handleCreateRun 创建 queued Run，并把任务投递给 Worker。
func (s *Server) handleCreateRun(c *gin.Context) {
	var req createRunRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		_ = c.Error(err)
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid json body"})
		return
	}
	if req.AgentID == "" {
		err := fmt.Errorf("agent_id is required")
		_ = c.Error(err)
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if len(req.Input) == 0 || string(req.Input) == "null" {
		err := fmt.Errorf("input is required")
		_ = c.Error(err)
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	now := time.Now().UTC()
	runID, err := newRunID()
	if err != nil {
		_ = c.Error(err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "generate run id failed"})
		return
	}

	run := store.RunRecord{
		RunID:     runID,
		AgentID:   req.AgentID,
		Input:     req.Input,
		Cacheable: req.Cacheable,
		CreatedAt: now,
	}
	// 先写 Redis 再发消息，保证调用方拿到 run_id 后能查到初始状态。
	if err := s.store.CreateRunQueued(c.Request.Context(), run); err != nil {
		_ = c.Error(err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "create run failed"})
		return
	}

	task := queue.TaskMessage{
		RunID:     runID,
		AgentID:   req.AgentID,
		Input:     req.Input,
		Cacheable: req.Cacheable,
		CreatedAt: now,
	}
	if err := s.publisher.PublishTask(c.Request.Context(), task); err != nil {
		_ = c.Error(err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "publish task failed"})
		return
	}

	c.JSON(http.StatusAccepted, gin.H{
		"run_id": runID,
		"status": store.StatusQueued,
	})
}

// handleGetRun 查询 Run 当前状态、最终结果和最近事件。
func (s *Server) handleGetRun(c *gin.Context) {
	runID := c.Param("run_id")
	if runID == "" {
		err := fmt.Errorf("run_id is required")
		_ = c.Error(err)
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	eventsLimit, err := parseEventsLimit(c.Query("events_limit"))
	if err != nil {
		_ = c.Error(err)
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	detail, err := s.store.GetRunDetail(c.Request.Context(), runID, eventsLimit)
	if err != nil {
		if errors.Is(err, store.ErrRunNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "run not found"})
			return
		}
		_ = c.Error(err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "get run failed"})
		return
	}

	c.JSON(http.StatusOK, detail)
}

// handleRunEvents 通过 SSE 推送指定 Run 的历史事件和实时事件。
// 建连后先注册本机 Hub，再读取 Redis 历史事件，并用 seq 去重避免注册期间产生重复事件。
func (s *Server) handleRunEvents(c *gin.Context) {
	runID := c.Param("run_id")
	if runID == "" {
		err := fmt.Errorf("run_id is required")
		_ = c.Error(err)
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	sub := s.sseHub.Subscribe(runID)
	defer sub.Close()

	if _, err := s.store.GetRunDetail(c.Request.Context(), runID, 0); err != nil {
		if errors.Is(err, store.ErrRunNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "run not found"})
			return
		}
		_ = c.Error(err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "get run failed"})
		return
	}

	events, err := s.store.ListRunEvents(c.Request.Context(), runID)
	if err != nil {
		_ = c.Error(err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "list run events failed"})
		return
	}

	prepareSSEHeaders(c)
	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		err := fmt.Errorf("response writer does not support flushing")
		_ = c.Error(err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "sse unsupported"})
		return
	}
	c.Status(http.StatusOK)

	var lastSeq int64
	for _, evt := range events {
		if err := writeSSEEvent(c, evt); err != nil {
			_ = c.Error(err)
			return
		}
		if evt.Seq > lastSeq {
			lastSeq = evt.Seq
		}
	}
	flusher.Flush()
	if len(events) > 0 && isTerminalRunEvent(events[len(events)-1]) {
		return
	}

	for {
		select {
		case <-c.Request.Context().Done():
			return
		case evt, ok := <-sub.Events():
			if !ok {
				return
			}
			if evt.Seq <= lastSeq {
				continue
			}
			if err := writeSSEEvent(c, evt); err != nil {
				_ = c.Error(err)
				return
			}
			flusher.Flush()
			lastSeq = evt.Seq
			if isTerminalRunEvent(evt) {
				return
			}
		}
	}
}

// newRunID 生成带 run_ 前缀的随机 ID，方便日志和 Redis key 排查。
func newRunID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("read random bytes: %w", err)
	}
	return "run_" + hex.EncodeToString(b[:]), nil
}

// parseEventsLimit 解析最近事件数量，默认不返回事件。
func parseEventsLimit(value string) (int64, error) {
	if value == "" {
		return 0, nil
	}

	limit, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("events_limit must be an integer")
	}
	if limit < 0 {
		return 0, fmt.Errorf("events_limit must be greater than or equal to 0")
	}
	if limit > 100 {
		return 0, fmt.Errorf("events_limit must be less than or equal to 100")
	}
	return limit, nil
}

// prepareSSEHeaders 设置 SSE 响应头。
func prepareSSEHeaders(c *gin.Context) {
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")
}

// writeSSEEvent 把标准 RunEvent 写成 SSE event/data 格式。
func writeSSEEvent(c *gin.Context, evt event.RunEvent) error {
	body, err := json.Marshal(evt)
	if err != nil {
		return fmt.Errorf("marshal sse event: %w", err)
	}

	if _, err := fmt.Fprintf(c.Writer, "event: %s\n", evt.Type); err != nil {
		return fmt.Errorf("write sse event type: %w", err)
	}
	if _, err := fmt.Fprintf(c.Writer, "data: %s\n\n", body); err != nil {
		return fmt.Errorf("write sse event data: %w", err)
	}
	return nil
}

// isTerminalRunEvent 判断事件是否表示一次 Run 已经结束。
func isTerminalRunEvent(evt event.RunEvent) bool {
	return evt.Type == event.EventTypeSucceeded || evt.Type == event.EventTypeFailed
}
