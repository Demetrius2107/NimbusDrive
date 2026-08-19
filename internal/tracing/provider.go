// Package tracing 封装 OpenTelemetry 分布式追踪的初始化与上下文传播。
//
// 设计目标：把追踪上下文贯穿 NimbusDrive 的所有边界——
//   - HTTP 边界（Gin/Hertz 入站提取 traceparent + 出站注入）
//   - 异步边界（Redis Streams 事件总线：Event 携带 TraceContext，Emitter 注入/Consumer 提取）
//   - 客户端中介边界（分享下载：ShareDownloadToken 携带 traceparent）
//   - pub/sub 边界（配额 SSE：QuotaChangeEvent 携带 traceparent）
//
// 导出可配置：stdout（默认，写 stderr，零外部依赖）/ otlp（gRPC 推 collector）/ none（no-op 降级）。
package tracing

import (
	"context"
	"errors"
	"fmt"
	"os"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.24.0"
	"go.opentelemetry.io/otel/trace"
)

// Exporter 常量。
const (
	ExporterStdout = "stdout" // 写 stderr（开发期默认）
	ExporterOTLP   = "otlp"   // gRPC 推 collector（如 Jaeger/Tempo）
	ExporterNone   = "none"   // no-op 降级（TracerProvider 不导出）
)

// Init 初始化全局 TracerProvider 并注册到 otel 全局。
// serviceName 为空时用 "nimbus" 兜底。返回的 provider 调用方应 defer Shutdown。
// exporter=none 时返回 no-op provider（不导出、不采样）。
func Init(serviceName, exporter, otlpEndpoint string, sampleRatio float64) (*sdktrace.TracerProvider, error) {
	if serviceName == "" {
		serviceName = "nimbus"
	}

	// 全局 propagator：W3C TraceContext + Baggage。Inject/Extract 用此。
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	res, err := resource.New(context.Background(),
		resource.WithAttributes(semconv.ServiceName(serviceName)),
	)
	if err != nil {
		return nil, fmt.Errorf("create trace resource: %w", err)
	}

	var exp sdktrace.SpanExporter
	switch exporter {
	case ExporterNone, "":
		// no-op：返回不导出的 provider，Tracer 操作为空操作。
		tp := sdktrace.NewTracerProvider(
			sdktrace.WithResource(res),
			sdktrace.WithSampler(sdktrace.NeverSample()),
		)
		otel.SetTracerProvider(tp)
		return tp, nil

	case ExporterStdout:
		exp, err = stdouttrace.New(stdouttrace.WithWriter(os.Stderr), stdouttrace.WithPrettyPrint())
		if err != nil {
			return nil, fmt.Errorf("create stdout exporter: %w", err)
		}

	case ExporterOTLP:
		opts := []otlptracegrpc.Option{otlptracegrpc.WithEndpoint(otlpEndpoint)}
		// 开发期 collector 通常无 TLS；生产应配 TLS。
		if otlpEndpoint == "" || isLocalhost(otlpEndpoint) {
			opts = append(opts, otlptracegrpc.WithInsecure())
		}
		exp, err = otlptracegrpc.New(context.Background(), opts...)
		if err != nil {
			return nil, fmt.Errorf("create otlp exporter: %w", err)
		}

	default:
		return nil, fmt.Errorf("unknown exporter %q (want stdout/otlp/none)", exporter)
	}

	// 采样：ParentBased(TraceIDRatioBased(ratio))。
	// 根 span 按 ratio 采样；子 span 跟随父决策（已采样的 trace 全采，未采样的全不采）。
	sampler := sdktrace.ParentBased(sdktrace.TraceIDRatioBased(clampRatio(sampleRatio)))

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sampler),
		sdktrace.WithBatcher(exp), // 异步批量导出
	)
	otel.SetTracerProvider(tp)
	return tp, nil
}

// Shutdown 优雅关闭 TracerProvider，刷新未导出的 span。main defer 调用。
func Shutdown(ctx context.Context, tp trace.TracerProvider) error {
	if tp == nil {
		return nil
	}
	sdkTP, ok := tp.(*sdktrace.TracerProvider)
	if !ok {
		return nil
	}
	return sdkTP.Shutdown(ctx)
}

// Tracer 返回全局 Tracer，name 为组件名（如 "eventbus" / "quota"）。
func Tracer(name string) trace.Tracer {
	return otel.Tracer(name)
}

// clampRatio 把 ratio 限制在 [0, 1]。
func clampRatio(r float64) float64 {
	if r < 0 {
		return 0
	}
	if r > 1 {
		return 1
	}
	return r
}

// isLocalhost 判断端点是否指向本地（用于决定是否用 insecure）。
func isLocalhost(endpoint string) bool {
	return len(endpoint) >= 9 &&
		(endpoint[:9] == "localhost" || endpoint[:3] == "127")
}

// ErrNoTracerProvider 当全局 TracerProvider 未初始化时返回。
var ErrNoTracerProvider = errors.New("tracer provider not initialized")
