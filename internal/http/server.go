package http

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"relay-flow/internal/logger"
)

// Server 封装 Gateway 的 HTTP 路由。
//
// 当前阶段只提供健康检查；后续任务提交、查询和 SSE 都会继续挂到同一个 router 上。
type Server struct {
	router *gin.Engine
}

// NewServer 创建 Gateway HTTP Server。
func NewServer() *Server {
	gin.SetMode(gin.ReleaseMode)

	router := gin.New()
	router.Use(logger.GinRequestLogger())
	router.Use(logger.GinRecovery())

	server := &Server{router: router}
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
}

func (s *Server) handleHealthz(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

func (s *Server) handleReadyz(c *gin.Context) {
	// readyz 表示 Gateway 已完成自身初始化，可以接收流量。
	// Redis/RabbitMQ 接入后，这里会扩展为真实依赖检查。
	c.JSON(http.StatusOK, gin.H{"status": "ready"})
}
