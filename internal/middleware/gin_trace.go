package middleware

import (
	"github.com/Demetrius2107/NimbusDrive/internal/tracing"
	"github.com/gin-gonic/gin"
	"go.opentelemetry.io/otel/propagation"
	semconv "go.opentelemetry.io/otel/semconv/v1.24.0"
	"go.opentelemetry.io/otel/trace"
)

// GinTrace 为每个请求起一个 server span，并传播 W3C traceparent。
//
// 挂载顺序：RequestID → Trace → Logger（Logger 用 FromContext 读 trace_id）。
// 入站：从请求头提取 traceparent（若有上游服务传入则续接 trace，否则起根 span）。
// 出站：span.End()。span 属性记录 HTTP method/route/status。
//
// 与 GinRequestID 共存：request_id 已由前者注入 gin.Context，
// 此处不重复，trace_id 作为独立维度叠加到日志。
func GinTracer(name string) gin.HandlerFunc {
	tracer := tracing.Tracer(name)
	return func(c *gin.Context) {
		// 从请求头提取 W3C trace context。
		ctx := tracing.ExtractHeaders(c.Request.Context(), propagation.HeaderCarrier(c.Request.Header))

		// 起 server span。operationName 用路由模板（如 /api/v1/files/:id）而非实际路径，
		// 避免高基数 trace（每个 file_id 一个 span name）。
		route := c.FullPath()
		if route == "" {
			route = c.Request.URL.Path
		}
		ctx, span := tracer.Start(ctx, route,
			trace.WithSpanKind(trace.SpanKindServer),
			trace.WithAttributes(
				semconv.HTTPMethod(c.Request.Method),
				semconv.HTTPRoute(route),
			),
		)

		// 把带 trace 的 ctx 注入 request，后续 handler/中间件用 c.Request.Context() 读到。
		c.Request = c.Request.WithContext(ctx)

		// 把 request_id 放进 baggage（可选：便于跨服务日志关联）。
		// 已由 GinRequestID 注入 gin.Context，此处不重复。

		c.Next()

		// 记录响应状态到 span，结束 span。
		span.SetAttributes(semconv.HTTPStatusCode(c.Writer.Status()))
		span.End()
	}
}
