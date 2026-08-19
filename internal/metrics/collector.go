// Package metrics 封装 Prometheus 指标仪器与 RED 中间件。
//
// 设计目标：把 NimbusDrive 的运行时信号量化为可抓取的时序数据，并与
// feat/observability-tracing 的 trace context 联动——每个 RED 观测点附带
// exemplar trace_id，Grafana 里从 P99 飙高点可直跳对应 trace。
//
// 三类指标：
//   - RED（基础设施层）：http_requests_total / http_request_duration_seconds /
//     http_requests_in_flight，由中间件自动采集
//   - 业务层：上传/下载/分享/认证/配额等语义信号，由 handler 显式埋点
//   - infra 层（scrape-time 自定义 Collector）：事件总线 emitter/consumer、
//     日志聚合器的 dropped/pending/dlq 计数，在 /metrics 被抓取时现查
//
// 全局单例模式（与 logger.L / tracing.Tracer 一致）：handler 调
// metrics.Default().UploadsCompleted.WithLabelValues(...).Inc()。
// Init 前调用返回 noop collector（仪器有效但不被任何 registry 抓取，nil 安全）。
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
)

// 跨域 histogram bucket：覆盖 APIServer 毫秒级请求、TransferServer 上传
// （最长 300s write_timeout）、SSE 长连接（分钟级）。一套 DefBuckets（最长 10s）
// 撑不住三域，故显式设计。取舍见 docs/protocol-specs/metrics.md。
var durationBuckets = prometheus.ExponentialBuckets(0.01, 2.5, 13) // ~0.01s..~316s

// Collector 持有所有 Prometheus 仪器。通过 Init 构造并注册到私有 Registry。
// 仪器字段公开供 handler 直接访问（WithLabelValues → Inc/Observe/Add）。
type Collector struct {
	Registry    *prometheus.Registry
	serviceName string

	// --- RED（基础设施层）---
	// label: service(api|transfer) / method / route(模板) / status_class(2xx|3xx|4xx|5xx)
	// route 用路由模板（c.FullPath()）而非原始 URL——基数控制核心：
	// 否则每个 file_id/share_token 进 path 产生新时序，几天爆内存。
	RequestsTotal    *prometheus.CounterVec
	RequestDuration  *prometheus.HistogramVec
	RequestsInFlight *prometheus.GaugeVec

	// --- 业务层：上传 ---
	// type=instant|multipart, result=success|error|quota_exceeded
	UploadsCompleted *prometheus.CounterVec
	// type=instant|multipart —— 上传字节数（counter 累加）
	UploadBytes *prometheus.CounterVec
	// 活跃上传会话数（gauge，create inc / complete|cancel dec）
	UploadActiveSessions prometheus.Gauge
	// 接收的分块数 + 分块字节数
	UploadChunksReceived *prometheus.CounterVec // label: 无 → 用 Counter 即可，但保持 Vec 一致性预留扩展
	// type=instant|multipart 会话创建数
	UploadSessionsCreated *prometheus.CounterVec
	// 上传取消数
	UploadCancellations prometheus.Counter

	// --- 业务层：下载 ---
	// mode=presign|stream|share, result=success|error|quota_exceeded|invalid
	DownloadsTotal *prometheus.CounterVec
	// mode=presign|stream|share —— 下载字节数
	// 注意：presign/share 只记签发时的 file.Size（近似），stream 记真实传输字节
	DownloadBytes *prometheus.CounterVec

	// --- 业务层：分享 ---
	// has_password=true|false
	SharesCreated *prometheus.CounterVec
	// result=success|password_wrong|exhausted|not_found
	ShareAccess *prometheus.CounterVec

	// --- 业务层：认证 ---
	// result=success|conflict|error
	UserRegistrations *prometheus.CounterVec
	// result=success|user_not_found|bad_password|disabled
	LoginAttempts *prometheus.CounterVec

	// --- 业务层：配额 ---
	// result=success|error
	QuotaMonthlyReset *prometheus.CounterVec
	// 管理员批量重置配额次数
	AdminQuotaResetAll prometheus.Counter
	// 活跃 SSE 连接数（gauge）
	SSEActiveConnections prometheus.Gauge
	// SSE 推送的事件数
	SSEEventsPushed prometheus.Counter
}

// newCollector 构造 Collector 并把所有仪器注册到 reg。仪器名统一 nimbus_ 前缀。
func newCollector(reg *prometheus.Registry) *Collector {
	c := &Collector{
		Registry: reg,
	}

	// --- RED ---
	c.RequestsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "nimbus_http_requests_total",
		Help: "HTTP requests processed, partitioned by service/method/route/status_class.",
	}, []string{"service", "method", "route", "status_class"})

	c.RequestDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "nimbus_http_request_duration_seconds",
		Help:    "HTTP request latency in seconds.",
		Buckets: durationBuckets,
	}, []string{"service", "method", "route", "status_class"})

	c.RequestsInFlight = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "nimbus_http_requests_in_flight",
		Help: "HTTP requests currently being processed.",
	}, []string{"service", "method", "route"})

	// --- 上传 ---
	c.UploadsCompleted = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "nimbus_uploads_completed_total",
		Help: "Uploads completed, partitioned by type(instant|multipart) and result.",
	}, []string{"type", "result"})

	c.UploadBytes = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "nimbus_upload_bytes_total",
		Help: "Total bytes uploaded.",
	}, []string{"type"})

	c.UploadActiveSessions = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "nimbus_upload_active_sessions",
		Help: "Active upload sessions (created but not completed/cancelled).",
	})

	c.UploadChunksReceived = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "nimbus_upload_chunks_received_total",
		Help: "Upload chunks received.",
	}, []string{})

	c.UploadSessionsCreated = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "nimbus_upload_sessions_created_total",
		Help: "Upload sessions created, partitioned by type.",
	}, []string{"type"})

	c.UploadCancellations = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "nimbus_upload_cancellations_total",
		Help: "Upload sessions cancelled.",
	})

	// --- 下载 ---
	c.DownloadsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "nimbus_downloads_total",
		Help: "Downloads, partitioned by mode(presign|stream|share) and result.",
	}, []string{"mode", "result"})

	c.DownloadBytes = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "nimbus_download_bytes_total",
		Help: "Total bytes downloaded (presign/share are approximate from file.Size; stream is actual).",
	}, []string{"mode"})

	// --- 分享 ---
	c.SharesCreated = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "nimbus_shares_created_total",
		Help: "Shares created, partitioned by has_password.",
	}, []string{"has_password"})

	c.ShareAccess = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "nimbus_share_access_total",
		Help: "Share accesses, partitioned by result.",
	}, []string{"result"})

	// --- 认证 ---
	c.UserRegistrations = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "nimbus_user_registrations_total",
		Help: "User registrations, partitioned by result.",
	}, []string{"result"})

	c.LoginAttempts = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "nimbus_login_attempts_total",
		Help: "Login attempts, partitioned by result.",
	}, []string{"result"})

	// --- 配额 ---
	c.QuotaMonthlyReset = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "nimbus_quota_monthly_reset_total",
		Help: "Monthly quota resets, partitioned by result.",
	}, []string{"result"})

	c.AdminQuotaResetAll = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "nimbus_admin_quota_reset_all_total",
		Help: "Admin bulk quota resets triggered.",
	})

	c.SSEActiveConnections = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "nimbus_sse_active_connections",
		Help: "Active SSE (quota stream) connections.",
	})

	c.SSEEventsPushed = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "nimbus_sse_events_pushed_total",
		Help: "SSE events pushed to clients.",
	})

	reg.MustRegister(
		c.RequestsTotal,
		c.RequestDuration,
		c.RequestsInFlight,
		c.UploadsCompleted,
		c.UploadBytes,
		c.UploadActiveSessions,
		c.UploadChunksReceived,
		c.UploadSessionsCreated,
		c.UploadCancellations,
		c.DownloadsTotal,
		c.DownloadBytes,
		c.SharesCreated,
		c.ShareAccess,
		c.UserRegistrations,
		c.LoginAttempts,
		c.QuotaMonthlyReset,
		c.AdminQuotaResetAll,
		c.SSEActiveConnections,
		c.SSEEventsPushed,
	)

	return c
}
