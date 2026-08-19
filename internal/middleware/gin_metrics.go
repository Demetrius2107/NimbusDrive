// Package middleware — Gin RED 指标中间件。
// 挂载顺序：RequestID → Tracer → Metrics → Logger → Recovery
// （Metrics 在 Tracer 后才能从 ctx 读 span context 附 exemplar）。
package middleware

import (
	"time"

	"github.com/Demetrius2107/NimbusDrive/internal/metrics"
	"github.com/gin-gonic/gin"
)

// unmatchedRoute 是无匹配路由时的 label 兜底。
// FullPath() 对未匹配路由返回 ""，直接用作 label 会产生空时序；
// 归一为 "/unmatched" 控基数（所有 404 共享一条时序）。
const unmatchedRoute = "/unmatched"

// GinMetrics 是 Gin RED 中间件。service=api|transfer。
// 自动采集每个请求的 requests_total / request_duration_seconds / requests_in_flight，
// 并从 ctx 的 active span 附 exemplar trace_id（Metrics ↔ Tracing 联动）。
func GinMetrics(service string) gin.HandlerFunc {
	return func(c *gin.Context) {
		route := c.FullPath()
		if route == "" {
			route = unmatchedRoute
		}
		method := c.Request.Method

		undo := metrics.TrackInFlight(service, method, route)
		defer undo()

		start := time.Now()
		c.Next()
		metrics.RecordRED(service, method, route, c.Writer.Status(), time.Since(start), c.Request.Context())
	}
}
