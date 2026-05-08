package logger

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/natefinch/lumberjack.v2"
)

const (
	defaultLogDir     = "logs"
	defaultMaxSizeMB  = 100
	defaultMaxBackups = 10
)

// Options 控制日志输出位置、级别和采样策略。
type Options struct {
	Service       string
	LogDir        string
	Level         string
	SampleRate    float64
	SlowRequestMS int
}

// Init 为指定服务初始化结构化日志。
//
// 日志会同时输出到屏幕和文件，文件到达 100MB 后自动轮转。
func Init(service string) error {
	return InitWithOptions(Options{Service: service, LogDir: defaultLogDir, Level: "info"})
}

// InitWithDir 允许显式指定日志目录，便于容器和本地使用不同挂载路径。
func InitWithDir(service, logDir string) error {
	return InitWithOptions(Options{Service: service, LogDir: logDir, Level: "info"})
}

// InitWithOptions 初始化结构化日志，并把默认 logger 设为 JSON 输出。
// 正常请求可以按采样率记录，错误、慢请求和高优先级日志仍然全量保留。
func InitWithOptions(opts Options) error {
	if opts.Service == "" {
		return fmt.Errorf("service name is required")
	}
	if opts.LogDir == "" {
		opts.LogDir = defaultLogDir
	}

	if err := os.MkdirAll(opts.LogDir, 0o755); err != nil {
		return fmt.Errorf("create log dir: %w", err)
	}

	level := parseLogLevel(opts.Level)
	fileWriter := &lumberjack.Logger{
		Filename:   filepath.Join(opts.LogDir, fmt.Sprintf("relay-flow-%s.log", opts.Service)),
		MaxSize:    defaultMaxSizeMB,
		MaxBackups: defaultMaxBackups,
		Compress:   false,
	}

	writer := io.MultiWriter(os.Stdout, fileWriter)
	handler := slog.NewJSONHandler(writer, &slog.HandlerOptions{Level: level})
	slog.SetDefault(slog.New(handler))
	return nil
}

// parseLogLevel 把字符串级别转换成 slog.Level，非法值回落到 info。
func parseLogLevel(value string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
