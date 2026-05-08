package observability

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

const tracerName = "relay-flow"

// InitTracing 初始化 OpenTelemetry Trace；未配置 endpoint 时使用 no-op provider。
func InitTracing(ctx context.Context, serviceName string) (func(context.Context) error, error) {
	endpoint := strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"))
	if endpoint == "" {
		otel.SetTextMapPropagator(propagation.TraceContext{})
		return func(context.Context) error { return nil }, nil
	}

	clientOptions := []otlptracehttp.Option{otlptracehttp.WithEndpointURL(endpoint)}
	if strings.EqualFold(os.Getenv("OTEL_EXPORTER_OTLP_INSECURE"), "true") {
		clientOptions = append(clientOptions, otlptracehttp.WithInsecure())
	}
	exporter, err := otlptracehttp.New(ctx, clientOptions...)
	if err != nil {
		return nil, fmt.Errorf("create otlp trace exporter: %w", err)
	}

	res, err := resource.Merge(
		resource.Default(),
		resource.NewWithAttributes(
			"",
			attribute.String("service.name", serviceName),
			attribute.String("service.namespace", "relayflow"),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("create trace resource: %w", err)
	}

	provider := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter, sdktrace.WithBatchTimeout(2*time.Second)),
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(provider)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	return provider.Shutdown, nil
}

// Tracer 返回 RelayFlow 统一使用的 tracer，避免各包各自命名。
func Tracer() trace.Tracer {
	return otel.Tracer(tracerName)
}

// InjectAMQPHeaders 把当前 span context 注入 RabbitMQ headers，用于跨队列恢复 trace。
func InjectAMQPHeaders(ctx context.Context, headers amqp.Table) amqp.Table {
	if headers == nil {
		headers = amqp.Table{}
	}
	otel.GetTextMapPropagator().Inject(ctx, amqpHeaderCarrier(headers))
	return headers
}

// ExtractAMQPContext 从 RabbitMQ headers 恢复 trace context。
func ExtractAMQPContext(ctx context.Context, headers amqp.Table) context.Context {
	return otel.GetTextMapPropagator().Extract(ctx, amqpHeaderCarrier(headers))
}

type amqpHeaderCarrier amqp.Table

// Get 按 OpenTelemetry TextMapCarrier 接口读取 RabbitMQ header。
func (c amqpHeaderCarrier) Get(key string) string {
	value, ok := c[key]
	if !ok {
		return ""
	}
	switch v := value.(type) {
	case string:
		return v
	case []byte:
		return string(v)
	default:
		return fmt.Sprint(v)
	}
}

// Set 按 OpenTelemetry TextMapCarrier 接口写入 RabbitMQ header。
func (c amqpHeaderCarrier) Set(key string, value string) {
	c[key] = value
}

// Keys 返回当前 carrier 中已有的 header 名称。
func (c amqpHeaderCarrier) Keys() []string {
	keys := make([]string, 0, len(c))
	for key := range c {
		keys = append(keys, key)
	}
	return keys
}
