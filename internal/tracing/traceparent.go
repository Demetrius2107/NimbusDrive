package tracing

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

// TraceParentString 从 ctx 提取当前 span 的 W3C traceparent 文本。
// 格式：00-<trace_id>-<span_id>-<flags>（32+16+2 hex，共 55 字符）。
// 用于把追踪上下文塞进单字符串字段（如 ShareDownloadToken.TraceParent）。
// 无 active span 时返回空串（调用方应 omitempty）。
func TraceParentString(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	sc := trace.SpanContextFromContext(ctx)
	if !sc.IsValid() {
		return ""
	}
	// W3C traceparent 格式：version-trace_id-parent_id-trace_flags
	return fmt.Sprintf("00-%s-%s-%s", sc.TraceID(), sc.SpanID(), sc.TraceFlags())
}

// ExtractFromTraceParent 从 traceparent 文本还原 trace context 到 ctx。
// 用于从单字符串字段（如 ShareDownloadToken.TraceParent）恢复追踪上下文。
// tp 为空或格式非法时原样返回 ctx。
//
// 注意：还原后 ctx 中的 SpanContext 的 SpanID 是"生产者"的 span，
// 调用方应起一个新 span（SpanKindConsumer/Internal），它会作为该 span 的 parent，
// 从而保持 trace 亲子关系（消费者 span 是生产者 span 的 follow-from child）。
func ExtractFromTraceParent(ctx context.Context, tp string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if tp == "" {
		return ctx
	}
	// 复用 W3C TraceContext propagator 的提取逻辑：
	// 构造一个只含 traceparent 头的 carrier，交给 propagator 解析。
	carrier := propagation.MapCarrier{"traceparent": tp}
	return otelPropagator().Extract(ctx, carrier)
}

// otelPropagator 返回全局 propagator（仅 TraceContext 部分，不含 Baggage，
// 因为 traceparent 单字符串只承载 trace context）。
func otelPropagator() propagation.TextMapPropagator {
	return propagation.TraceContext{}
}
