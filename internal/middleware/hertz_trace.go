package middleware

import (
	"context"

	"github.com/Demetrius2107/NimbusDrive/internal/tracing"
	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/protocol"
	semconv "go.opentelemetry.io/otel/semconv/v1.24.0"
	"go.opentelemetry.io/otel/trace"
)

// hertzHeaderCarrier 适配 Hertz 的 protocol.RequestHeader 为 otel TextMapCarrier。
type hertzHeaderCarrier struct{ h *protocol.RequestHeader }

func (c hertzHeaderCarrier) Get(key string) string { return string(c.h.Peek(key)) }
func (c hertzHeaderCarrier) Set(key, val string)  { c.h.Set(key, val) }
func (c hertzHeaderCarrier) Keys() []string {
	// Hertz RequestHeader 无直接 Keys()，返回 W3C propagator 关心的头即可。
	return []string{"traceparent", "tracestate"}
}

// HertzTracer 为每个请求起 server span，传播 W3C traceparent。
// 与 GinTracer 对应。挂载顺序：RequestID → Trace → Logger。
func HertzTracer(name string) app.HandlerFunc {
	tracer := tracing.Tracer(name)
	return func(ctx context.Context, c *app.RequestContext) {
		// 从请求头提取 W3C trace context。
		carrier := hertzHeaderCarrier{h: &c.Request.Header}
		ctx = tracing.ExtractHeaders(ctx, carrier)

		route := string(c.Request.URI().Path())
		ctx, span := tracer.Start(ctx, route,
			trace.WithSpanKind(trace.SpanKindServer),
			trace.WithAttributes(
				semconv.HTTPMethod(string(c.Request.Method())),
				semconv.HTTPRoute(route),
			),
		)

		c.Next(ctx)

		span.SetAttributes(semconv.HTTPStatusCode(c.Response.StatusCode()))
		span.End()
	}
}
