// Package middleware — Hertz RED 指标中间件。
// 与 gin_metrics.go 逻辑对应，适配 Hertz 的 app.RequestContext。
package middleware

import (
	"context"
	"time"

	"github.com/Demetrius2107/NimbusDrive/internal/metrics"
	"github.com/cloudwego/hertz/pkg/app"
)

// HertzMetrics 是 Hertz RED 中间件。service=api|transfer。
// route 用 c.FullPath()（Hertz 在 route.go 匹配时 SetFullPath），
// 无匹配回退 "/unmatched" 控基数。exemplar 从 ctx 的 active span 提取。
func HertzMetrics(service string) app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		route := string(c.FullPath())
		if route == "" {
			route = unmatchedRoute
		}
		method := string(c.Request.Method())

		undo := metrics.TrackInFlight(service, method, route)
		defer undo()

		start := time.Now()
		c.Next(ctx)
		metrics.RecordRED(service, method, route, c.Response.StatusCode(), time.Since(start), ctx)
	}
}
