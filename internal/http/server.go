package http

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"relay-flow/internal/logger"
	"relay-flow/internal/queue"
	"relay-flow/internal/store"
)

// Server 封装 Gateway 的 HTTP 路由。
//
// 当前阶段只提供健康检查；后续任务提交、查询和 SSE 都会继续挂到同一个 router 上。
type Server struct {
	router    *gin.Engine
	store     RunStore
	publisher TaskPublisher
}

type RunStore interface {
	CreateRunQueued(ctx context.Context, run store.RunRecord) error
}

type TaskPublisher interface {
	PublishTask(ctx context.Context, task queue.TaskMessage) error
}

type Dependencies struct {
	Store     RunStore
	Publisher TaskPublisher
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
	}
	server.registerRoutes()
	return server
}

// Handler 返回标准库 http.Handler，方便测试和后续接入 net/http Server。
func (s *Server) Handler() http.Handler {
	return s.router
}

func (s *Server) registerRoutes() {
	s.router.GET("/healthz", s.handleHealthz)
	s.router.GET("/readyz", s.handleReadyz)
	s.router.POST("/v1/runs", s.handleCreateRun)
}

func (s *Server) handleHealthz(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

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

func newRunID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("read random bytes: %w", err)
	}
	return "run_" + hex.EncodeToString(b[:]), nil
}
