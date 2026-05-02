package logger

import (
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

// GinRequestLogger 统一记录 Gin 请求日志。
func GinRequestLogger() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()

		attrs := []any{
			"method", c.Request.Method,
			"path", c.FullPath(),
			"status", c.Writer.Status(),
			"ip", c.ClientIP(),
			"latency", time.Since(start),
			"size", c.Writer.Size(),
		}

		if len(c.Errors) > 0 {
			attrs = append(attrs, "err", c.Errors.String())
			slog.Error("http request failed", attrs...)
			return
		}

		slog.Info("http request", attrs...)
	}
}

// GinRecovery 捕获 Gin handler panic，并统一用 slog 记录。
func GinRecovery() gin.HandlerFunc {
	return gin.CustomRecovery(func(c *gin.Context, recovered any) {
		slog.Error("http panic recovered", "panic", fmt.Sprint(recovered))
		c.AbortWithStatus(http.StatusInternalServerError)
	})
}
