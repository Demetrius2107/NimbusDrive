package metrics

import (
	"sync"
	"testing"

	"github.com/Demetrius2107/NimbusDrive/internal/eventbus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// TestInterfaceCompliance 编译期断言：*eventbus.Emitter 和 *eventbus.Consumer
// 实现 InfraSources 接口。若后续重构破坏签名，编译即失败。
func TestInterfaceCompliance(t *testing.T) {
	var _ EmitterMetrics = (*eventbus.Emitter)(nil)
	var _ ConsumerMetrics = (*eventbus.Consumer)(nil)
}

// TestDefault_NoInit_NoOpNilSafe 验证 Init 前调用 Default() 返回 noop collector，
// 仪器操作不 panic（与 logger.L / tracing.Tracer 的 nil 安全语义一致）。
// noop collector 的仪器是真实 CounterVec（注册到私有 noop registry），
// Inc 会改内部值但不被任何 /metrics 抓取——"nil 安全"指不 panic，非不计数。
func TestDefault_NoInit_NoOpNilSafe(t *testing.T) {
	Shutdown() // 确保未 Init 状态
	c := Default()
	// 这些调用都不应 panic
	c.UploadsCompleted.WithLabelValues("instant", "success").Inc()
	c.UploadBytes.WithLabelValues("multipart").Add(1024)
	c.UploadActiveSessions.Inc()
	c.UploadActiveSessions.Dec()
	c.DownloadsTotal.WithLabelValues("presign", "success").Inc()
	c.SSEActiveConnections.Inc()
	c.SSEEventsPushed.Inc()
	// 验证 Registry 是 noop 的（非 Init 返回的 collector 的 Registry）
	if c.Registry == nil {
		t.Error("noop collector Registry should not be nil")
	}
}

// TestInit_InstrumentsRegistered 验证 Init 后仪器可被 ToFloat64 读取（已注册到 registry）。
func TestInit_InstrumentsRegistered(t *testing.T) {
	defer Shutdown()
	c := Init("test-api")
	c.UploadsCompleted.WithLabelValues("instant", "success").Inc()
	c.UploadsCompleted.WithLabelValues("instant", "success").Inc()
	c.UploadsCompleted.WithLabelValues("multipart", "error").Inc()

	if got := testutil.ToFloat64(c.UploadsCompleted.WithLabelValues("instant", "success")); got != 2 {
		t.Errorf("uploads_completed{instant,success} = %v, want 2", got)
	}
	if got := testutil.ToFloat64(c.UploadsCompleted.WithLabelValues("multipart", "error")); got != 1 {
		t.Errorf("uploads_completed{multipart,error} = %v, want 1", got)
	}
}

// TestInit_ConcurrentInc 并发安全：N goroutine 同时 Inc，总量应等于 N。
// 仿 eventbus/emitter_test.go 的 WaitGroup 模式。
func TestInit_ConcurrentInc(t *testing.T) {
	defer Shutdown()
	c := Init("test-concurrent")

	const n = 100
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			c.LoginAttempts.WithLabelValues("success").Inc()
		}()
	}
	wg.Wait()

	if got := testutil.ToFloat64(c.LoginAttempts.WithLabelValues("success")); got != n {
		t.Errorf("concurrent login_attempts = %v, want %d", got, n)
	}
}

// TestStatusClass 验证状态码归并：控基数的核心——4 类而非原始码。
func TestStatusClass(t *testing.T) {
	cases := []struct {
		code int
		want string
	}{
		{200, "2xx"}, {201, "2xx"}, {204, "2xx"},
		{301, "3xx"}, {302, "3xx"},
		{400, "4xx"}, {401, "4xx"}, {403, "4xx"}, {404, "4xx"}, {409, "4xx"},
		{500, "5xx"}, {502, "5xx"}, {503, "5xx"},
		{100, "1xx"},
	}
	for _, tc := range cases {
		if got := statusClass(tc.code); got != tc.want {
			t.Errorf("statusClass(%d) = %q, want %q", tc.code, got, tc.want)
		}
	}
}
