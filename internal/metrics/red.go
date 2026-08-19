package metrics

import (
	"context"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel/trace"
)

// exemplarLabelKey 是 exemplar 里 trace_id 的键名。Prometheus exemplar 至少需 1 个标签。
const exemplarLabelKey = "trace_id"

// exemplarFromContext 从活跃 span 提取 trace_id 作为 exemplar 标签。
// 无有效 span（未采样 / 未初始化 tracing）时返回 nil——此时 ObserveWithExemplar
// 退化为普通 Observe，不附 exemplar。这是 Metrics ↔ Tracing 联动的关键：
// Grafana 里 P99 飙高的桶点 exemplar 可直跳 Tempo/Jaeger 对应 trace。
func exemplarFromContext(ctx context.Context) prometheus.Labels {
	if ctx == nil {
		return nil
	}
	sc := trace.SpanContextFromContext(ctx)
	if !sc.IsValid() {
		return nil
	}
	return prometheus.Labels{exemplarLabelKey: sc.TraceID().String()}
}

// statusClass 把 HTTP 状态码归并为基数可控的类。
// 不用原始状态码做 label——否则 200/201/204/301/302/400/401/403/404/409/500/502/503
// 各一个时序，且未来新增状态码自动膨胀。归 2xx/3xx/4xx/5xx 四类足够做 RED 分析。
func statusClass(code int) string {
	switch {
	case code >= 200 && code < 300:
		return "2xx"
	case code >= 300 && code < 400:
		return "3xx"
	case code >= 400 && code < 500:
		return "4xx"
	case code >= 500:
		return "5xx"
	default:
		return "1xx"
	}
}

// RecordRED 记录一次 HTTP 请求的 RED 指标，并附带 exemplar（如有 trace）。
// service=api|transfer，route 须为路由模板（c.FullPath()），非原始 URL。
// 由 Gin/Hertz 中间件在 c.Next() 后调用。
func RecordRED(service, method, route string, status int, duration time.Duration, ctx context.Context) {
	c := Default()
	class := statusClass(status)
	ex := exemplarFromContext(ctx)

	// Counter：有 exemplar 用 AddWithExemplar（等价 Add + 附 trace_id），
	// 无 exemplar 退化为普通 Add。两条路径互斥，避免重复计数。
	if ex != nil {
		if h, ok := c.RequestsTotal.WithLabelValues(service, method, route, class).(prometheus.ExemplarAdder); ok {
			h.AddWithExemplar(1, ex)
		} else {
			c.RequestsTotal.WithLabelValues(service, method, route, class).Add(1)
		}
	} else {
		c.RequestsTotal.WithLabelValues(service, method, route, class).Add(1)
	}
	// Histogram：ObserveWithExemplar 接受 nil exemplar，退化为普通 Observe
	c.RequestDuration.WithLabelValues(service, method, route, class).(prometheus.ExemplarObserver).ObserveWithExemplar(duration.Seconds(), ex)
}

// TrackInFlight 标记请求开始处理（in-flight +1）。返回 undo 函数在请求结束时调（-1）。
func TrackInFlight(service, method, route string) func() {
	g := Default().RequestsInFlight.WithLabelValues(service, method, route)
	g.Inc()
	return func() { g.Dec() }
}
