package logger

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"gopkg.in/natefinch/lumberjack.v2"
)

const (
	defaultLogDir     = "logs"
	defaultMaxSizeMB  = 100
	defaultMaxBackups = 10
)

// Init 为指定服务初始化结构化日志。
//
// 日志会同时输出到屏幕和文件，文件到达 100MB 后自动轮转。
func Init(service string) error {
	return InitWithDir(service, defaultLogDir)
}

// InitWithDir 允许显式指定日志目录，便于容器和本地使用不同挂载路径。
func InitWithDir(service, logDir string) error {
	if service == "" {
		return fmt.Errorf("service name is required")
	}
	if logDir == "" {
		logDir = defaultLogDir
	}

	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return fmt.Errorf("create log dir: %w", err)
	}

	fileWriter := &lumberjack.Logger{
		Filename:   filepath.Join(logDir, fmt.Sprintf("relay-flow-%s.log", service)),
		MaxSize:    defaultMaxSizeMB,
		MaxBackups: defaultMaxBackups,
		Compress:   false,
	}

	writer := io.MultiWriter(os.Stdout, fileWriter)
	handler := slog.NewJSONHandler(writer, &slog.HandlerOptions{Level: slog.LevelInfo})
	slog.SetDefault(slog.New(handler))
	return nil
}
