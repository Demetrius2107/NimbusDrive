package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Demetrius2107/NimbusDrive/internal/metrics"
	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// TestGinMetrics_REDAndCardinality 验证 RED 中间件的核心行为：
//  1. requests_total 有计数
//  2. route label 是模板（/api/v1/files/:id）而非原始 URL（/api/v1/files/123）
//     —— 基数控制核心：否则每个 file_id 产生新时序
//  3. status_class 归并（200→2xx）
//  4. in-flight gauge 归零
func TestGinMetrics_REDAndCardinality(t *testing.T) {
	defer metrics.Shutdown()
	c := metrics.Init("test-red")
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(GinMetrics("api"))
	r.GET("/api/v1/files/:id", func(ctx *gin.Context) {
		ctx.Status(http.StatusOK)
	})
	r.GET("/healthz", func(ctx *gin.Context) {
		ctx.Status(http.StatusOK)
	})

	// 两个不同 file_id 的请求 → 应归并到同一路由模板时序
	req1 := httptest.NewRequest(http.MethodGet, "/api/v1/files/123", nil)
	r.ServeHTTP(httptest.NewRecorder(), req1)

	req2 := httptest.NewRequest(http.MethodGet, "/api/v1/files/456", nil)
	r.ServeHTTP(httptest.NewRecorder(), req2)

	req3 := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	r.ServeHTTP(httptest.NewRecorder(), req3)

	// 基数控制：123 和 456 归并到 /api/v1/files/:id 模板
	got := testutil.ToFloat64(c.RequestsTotal.WithLabelValues("api", "GET", "/api/v1/files/:id", "2xx"))
	if got != 2 {
		t.Errorf("requests_total{api,GET,/api/v1/files/:id,2xx} = %v, want 2 (cardinality: 123+456 merged)", got)
	}

	got = testutil.ToFloat64(c.RequestsTotal.WithLabelValues("api", "GET", "/healthz", "2xx"))
	if got != 1 {
		t.Errorf("requests_total{api,GET,/healthz,2xx} = %v, want 1", got)
	}

	// in-flight 归零
	got = testutil.ToFloat64(c.RequestsInFlight.WithLabelValues("api", "GET", "/api/v1/files/:id"))
	if got != 0 {
		t.Errorf("requests_in_flight should be 0 after request done, got %v", got)
	}
}

// TestGinMetrics_StatusClassBucketing 验证状态码归并到正确 status_class。
func TestGinMetrics_StatusClassBucketing(t *testing.T) {
	defer metrics.Shutdown()
	c := metrics.Init("test-status")
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(GinMetrics("api"))
	r.GET("/ok", func(ctx *gin.Context) { ctx.Status(http.StatusOK) })
	r.GET("/notfound", func(ctx *gin.Context) { ctx.Status(http.StatusNotFound) })
	r.GET("/error", func(ctx *gin.Context) { ctx.Status(http.StatusInternalServerError) })

	for _, path := range []string{"/ok", "/notfound", "/error"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		r.ServeHTTP(httptest.NewRecorder(), req)
	}

	if got := testutil.ToFloat64(c.RequestsTotal.WithLabelValues("api", "GET", "/ok", "2xx")); got != 1 {
		t.Errorf("2xx bucket = %v, want 1", got)
	}
	if got := testutil.ToFloat64(c.RequestsTotal.WithLabelValues("api", "GET", "/notfound", "4xx")); got != 1 {
		t.Errorf("4xx bucket = %v, want 1", got)
	}
	if got := testutil.ToFloat64(c.RequestsTotal.WithLabelValues("api", "GET", "/error", "5xx")); got != 1 {
		t.Errorf("5xx bucket = %v, want 1", got)
	}
}

// TestGinMetrics_UnmatchedRoute 验证无匹配路由归一为 /unmatched（控基数）。
func TestGinMetrics_UnmatchedRoute(t *testing.T) {
	defer metrics.Shutdown()
	c := metrics.Init("test-unmatched")
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(GinMetrics("api"))
	// 不注册任何路由，所有请求都是 404

	req := httptest.NewRequest(http.MethodGet, "/nonexistent/path/123", nil)
	r.ServeHTTP(httptest.NewRecorder(), req)

	got := testutil.ToFloat64(c.RequestsTotal.WithLabelValues("api", "GET", "/unmatched", "4xx"))
	if got != 1 {
		t.Errorf("unmatched route should be labeled /unmatched, got %v", got)
	}
}
