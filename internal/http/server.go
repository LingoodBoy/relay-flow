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
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"

	"relay-flow/internal/event"
	"relay-flow/internal/logger"
	"relay-flow/internal/observability"
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
	s.router.GET("/metrics", gin.WrapH(promhttp.Handler()))
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
	ctx, span := observability.Tracer().Start(c.Request.Context(), "gateway.create_run")
	defer span.End()

	var req createRunRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "invalid json body")
		_ = c.Error(err)
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid json body"})
		return
	}
	if req.AgentID == "" {
		err := fmt.Errorf("agent_id is required")
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		_ = c.Error(err)
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if len(req.Input) == 0 || string(req.Input) == "null" {
		err := fmt.Errorf("input is required")
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		_ = c.Error(err)
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	now := time.Now().UTC()
	runID, err := newRunID()
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "generate run id failed")
		_ = c.Error(err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "generate run id failed"})
		return
	}
	span.SetAttributes(attribute.String("run_id", runID), attribute.String("agent_id", req.AgentID))

	run := store.RunRecord{
		RunID:     runID,
		AgentID:   req.AgentID,
		Input:     req.Input,
		Cacheable: req.Cacheable,
		CreatedAt: now,
	}
	// 先写 Redis 再发消息，保证调用方拿到 run_id 后能查到初始状态。
	if err := s.store.CreateRunQueued(ctx, run); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "create run failed")
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
		Attempt:   1,
	}
	if err := s.publisher.PublishTask(ctx, task); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "publish task failed")
		_ = c.Error(err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "publish task failed"})
		return
	}
	observability.TaskSubmittedTotal.Inc()

	c.JSON(http.StatusAccepted, gin.H{
		"run_id": runID,
		"status": store.StatusQueued,
	})
}

// handleGetRun 查询 Run 当前状态、最终结果和最近事件。
func (s *Server) handleGetRun(c *gin.Context) {
	ctx, span := observability.Tracer().Start(c.Request.Context(), "gateway.get_run")
	defer span.End()

	runID := c.Param("run_id")
	if runID == "" {
		err := fmt.Errorf("run_id is required")
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		_ = c.Error(err)
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	eventsLimit, err := parseEventsLimit(c.Query("events_limit"))
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		_ = c.Error(err)
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	span.SetAttributes(attribute.String("run_id", runID))
	detail, err := s.store.GetRunDetail(ctx, runID, eventsLimit)
	if err != nil {
		if errors.Is(err, store.ErrRunNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "run not found"})
			return
		}
		span.RecordError(err)
		span.SetStatus(codes.Error, "get run failed")
		_ = c.Error(err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "get run failed"})
		return
	}

	c.JSON(http.StatusOK, detail)
}

// handleRunEvents 通过 SSE 推送指定 Run 的历史事件和实时事件。
// 建连后先注册本机 Hub，再读取 Redis 历史事件，并用 seq 去重避免注册期间产生重复事件。
func (s *Server) handleRunEvents(c *gin.Context) {
	ctx, span := observability.Tracer().Start(c.Request.Context(), "gateway.sse_events")
	defer span.End()

	runID := c.Param("run_id")
	if runID == "" {
		err := fmt.Errorf("run_id is required")
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		_ = c.Error(err)
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	span.SetAttributes(attribute.String("run_id", runID))
	observability.SSEConnections.Inc()
	defer observability.SSEConnections.Dec()
	defer observability.SSEDisconnectsTotal.Inc()

	sub := s.sseHub.Subscribe(runID)
	defer sub.Close()

	if _, err := s.store.GetRunDetail(ctx, runID, 0); err != nil {
		if errors.Is(err, store.ErrRunNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "run not found"})
			return
		}
		span.RecordError(err)
		span.SetStatus(codes.Error, "get run failed")
		_ = c.Error(err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "get run failed"})
		return
	}

	events, err := s.store.ListRunEvents(ctx, runID)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "list run events failed")
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
		if err := writeSSEEvent(ctx, c, evt); err != nil {
			span.RecordError(err)
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

	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-c.Request.Context().Done():
			return
		case <-heartbeat.C:
			if _, err := fmt.Fprint(c.Writer, ": ping\n\n"); err != nil {
				_ = c.Error(fmt.Errorf("write sse heartbeat: %w", err))
				return
			}
			flusher.Flush()
		case evt, ok := <-sub.Events():
			if !ok {
				return
			}
			if evt.Seq <= lastSeq {
				continue
			}
			if err := writeSSEEvent(ctx, c, evt); err != nil {
				span.RecordError(err)
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
func writeSSEEvent(ctx context.Context, c *gin.Context, evt event.RunEvent) error {
	_, span := observability.Tracer().Start(ctx, "gateway.sse_write")
	defer span.End()
	span.SetAttributes(
		attribute.String("run_id", evt.RunID),
		attribute.Int64("event.seq", evt.Seq),
		attribute.String("event.type", string(evt.Type)),
	)

	body, err := json.Marshal(evt)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "marshal sse event")
		return fmt.Errorf("marshal sse event: %w", err)
	}

	if _, err := fmt.Fprintf(c.Writer, "event: %s\n", evt.Type); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "write sse event type")
		return fmt.Errorf("write sse event type: %w", err)
	}
	if _, err := fmt.Fprintf(c.Writer, "data: %s\n\n", body); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "write sse event data")
		return fmt.Errorf("write sse event data: %w", err)
	}
	return nil
}

// isTerminalRunEvent 判断事件是否表示一次 Run 已经结束。
func isTerminalRunEvent(evt event.RunEvent) bool {
	return evt.Type == event.EventTypeSucceeded ||
		evt.Type == event.EventTypeFailed ||
		evt.Type == event.EventTypeDeadLetter
}
