package logger

import (
	"fmt"
	"log/slog"
	"math/rand"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

// GinRequestLogger 统一记录 Gin 请求日志。
func GinRequestLogger() gin.HandlerFunc {
	return GinRequestLoggerWithConfig(HTTPLogConfig{SampleRate: 1, SlowRequest: time.Second})
}

// HTTPLogConfig 控制 Gin 请求日志的采样和慢请求阈值。
type HTTPLogConfig struct {
	SampleRate  float64
	SlowRequest time.Duration
}

// GinRequestLoggerWithConfig 按状态码、耗时和采样率记录请求日志。
// 正常请求只做采样，错误请求和慢请求全量保留。
func GinRequestLoggerWithConfig(cfg HTTPLogConfig) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		status := c.Writer.Status()
		latency := time.Since(start)

		attrs := []any{
			"method", c.Request.Method,
			"path", c.FullPath(),
			"status", status,
			"ip", c.ClientIP(),
			"latency", latency,
			"size", c.Writer.Size(),
		}

		if len(c.Errors) > 0 {
			attrs = append(attrs, "err", c.Errors.String())
			ErrorContext(c.Request.Context(), "http request failed", attrs...)
			return
		}

		if status >= http.StatusInternalServerError {
			ErrorContext(c.Request.Context(), "http request failed", attrs...)
			return
		}
		if latency >= cfg.SlowRequest {
			WarnContext(c.Request.Context(), "slow http request", attrs...)
			return
		}
		if shouldSample(cfg.SampleRate) {
			slog.Info("http request", attrs...)
		}
	}
}

// GinRecovery 捕获 Gin handler panic，并统一用 slog 记录。
func GinRecovery() gin.HandlerFunc {
	return gin.CustomRecovery(func(c *gin.Context, recovered any) {
		slog.Error("http panic recovered", "panic", fmt.Sprint(recovered))
		c.AbortWithStatus(http.StatusInternalServerError)
	})
}

// shouldSample 根据采样率决定是否记录正常请求日志。
func shouldSample(rate float64) bool {
	if rate <= 0 {
		return false
	}
	if rate >= 1 {
		return true
	}
	return rand.Float64() < rate
}
