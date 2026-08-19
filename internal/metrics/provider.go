package metrics

import (
	"sync"

	"github.com/prometheus/client_golang/prometheus"
)

// 全局单例。Init 在启动期（单 goroutine）调用一次，Default 在 handler goroutine 读。
// 用 RWMutex 保护：读多写少，RLock 路径无竞争。
var (
	mu               sync.RWMutex
	defaultCollector *Collector

	// noopCollector 在包初始化时创建一次，仪器有效但不被任何 registry 抓取。
	// Init 前调用 Default() 返回它——Inc/Observe 是合法 no-op，不 panic。
	noopCollector = newCollector(prometheus.NewRegistry())
)

// Init 构造并注册全局 Collector。serviceName 仅用于 infra collector 的 resource
// 属性（如有）。返回的 *Collector 持有私有 Registry，供 main 挂载 /metrics 端点
// 和注入 infra collector。
//
// 重复调用：后一次覆盖前一次（前一个 Registry 被 GC）。main 启动期调用一次即可。
func Init(serviceName string) *Collector {
	c := newCollector(prometheus.NewRegistry())
	c.serviceName = serviceName
	mu.Lock()
	defaultCollector = c
	mu.Unlock()
	return c
}

// Shutdown 置空全局 collector。main defer 调用。此后 Default 返回 noop。
func Shutdown() {
	mu.Lock()
	defaultCollector = nil
	mu.Unlock()
}

// Default 返回全局 collector；未 Init 时返回 noop（nil 安全）。
// handler 通过 metrics.Default().Xxx.WithLabelValues(...).Inc() 访问。
func Default() *Collector {
	mu.RLock()
	c := defaultCollector
	mu.RUnlock()
	if c != nil {
		return c
	}
	return noopCollector
}

// Registry 返回全局 collector 的 Registry；未 Init 时返回 noop 的（供 main 判断）。
func Registry() *prometheus.Registry {
	return Default().Registry
}
