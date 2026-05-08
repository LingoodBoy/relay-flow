package logger

import (
	"context"
	"log/slog"

	"go.opentelemetry.io/otel/trace"
)

// TraceAttrs 从 context 里提取 trace_id 和 span_id，方便日志跳转到 Jaeger 排查。
// 如果当前 context 没有有效 span，则返回空切片，避免日志里出现无意义字段。
func TraceAttrs(ctx context.Context) []any {
	spanCtx := trace.SpanContextFromContext(ctx)
	if !spanCtx.IsValid() {
		return nil
	}

	return []any{
		"trace_id", spanCtx.TraceID().String(),
		"span_id", spanCtx.SpanID().String(),
	}
}

// ErrorContext 记录带 trace_id 的错误日志。
func ErrorContext(ctx context.Context, msg string, attrs ...any) {
	attrs = append(attrs, TraceAttrs(ctx)...)
	slog.Error(msg, attrs...)
}

// WarnContext 记录带 trace_id 的告警日志。
func WarnContext(ctx context.Context, msg string, attrs ...any) {
	attrs = append(attrs, TraceAttrs(ctx)...)
	slog.Warn(msg, attrs...)
}

// InfoContext 记录带 trace_id 的信息日志。
func InfoContext(ctx context.Context, msg string, attrs ...any) {
	attrs = append(attrs, TraceAttrs(ctx)...)
	slog.Info(msg, attrs...)
}
