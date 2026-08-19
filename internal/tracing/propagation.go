package tracing

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
)

// mapCarrier 让 map[string]string 实现 otel propagation.TextMapCarrier，
// 用于把 W3C traceparent/tracestate 编码进 domain.Event 跨 Redis Streams 传播。
type mapCarrier map[string]string

func (m mapCarrier) Get(key string) string { return m[key] }

func (m mapCarrier) Set(key, value string) { m[key] = value }

func (m mapCarrier) Keys() []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

// Inject 从 ctx 提取 W3C trace context（traceparent + tracestate）写入返回的 map。
// 用于事件 Emitter 在 Emit 时把当前 span 的追踪上下文序列化进 Event.TraceContext。
// 无 active span 时返回 nil（调用方应 omitempty）。
func Inject(ctx context.Context) map[string]string {
	if ctx == nil {
		return nil
	}
	m := mapCarrier{}
	otel.GetTextMapPropagator().Inject(ctx, propagation.TextMapCarrier(m))
	if len(m) == 0 {
		return nil
	}
	return map[string]string(m)
}

// Extract 从 map 还原 W3C trace context 到 ctx，返回新 ctx。
// 用于事件 Consumer 解码后恢复追踪上下文，起 consumer span 作为子 span。
// m 为空或 nil 时原样返回 ctx（无追踪上下文，span 仍是根）。
func Extract(ctx context.Context, m map[string]string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if len(m) == 0 {
		return ctx
	}
	return otel.GetTextMapPropagator().Extract(ctx, propagation.TextMapCarrier(mapCarrier(m)))
}

// InjectHeaders 从 ctx 提取 trace context 写入 HTTP 头（propagation.HeaderCarrier 适配）。
// 用于 HTTP 出站请求注入 traceparent。
func InjectHeaders(ctx context.Context, carrier propagation.TextMapCarrier) {
	if ctx == nil {
		return
	}
	otel.GetTextMapPropagator().Inject(ctx, carrier)
}

// ExtractHeaders 从 HTTP 头提取 trace context 返回新 ctx。
// 用于 HTTP 入站请求提取 traceparent。
func ExtractHeaders(ctx context.Context, carrier propagation.TextMapCarrier) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return otel.GetTextMapPropagator().Extract(ctx, carrier)
}
