# NimbusDrive 指标协议规范

> 分支：`feat/observability-metrics`。补齐可观测性三支柱中的 Metrics，并与 feat/observability-tracing 的 Trace 联动。

## 一、设计目标

把 NimbusDrive 的运行时信号量化为可抓取的时序数据，并通过 **exemplar** 把指标和追踪联动成一根链——Grafana 里从 P99 延迟飙高的桶点可直跳对应 trace，从 trace 可跳对应日志。

三类指标：
- **RED（基础设施层）**：`http_requests_total` / `http_request_duration_seconds` / `http_requests_in_flight`，由中间件自动采集
- **业务层**：上传/下载/分享/认证/配额等语义信号，由 handler 显式埋点
- **infra 层（scrape-time 自定义 Collector）**：事件总线 emitter/consumer、日志聚合器的 dropped/pending/dlq 计数，在 `/metrics` 被抓取时现查

## 二、深挖点

### 1. 基数控制（Cardinality）

RED 指标的 `route` label 用**路由模板**（`c.FullPath()` → `/api/v1/files/:id`）而非原始 URL。否则每个 `file_id` / `share_token` 进 path 就产生一个新时序，几天爆内存。

`status` label 归并为状态码类（`2xx`/`3xx`/`4xx`/`5xx`），而非原始状态码——否则 200/201/204/301/302/400/401/403/404/409/500/502/503 各一个时序，且未来新增状态码自动膨胀。

无匹配路由归一为 `/unmatched`（所有 404 共享一条时序）。

业务指标的 label 维度刻意低基数：`type`（instant|multipart）/ `mode`（presign|stream|share）/ `result` / `has_password`——**不含** `user_id` / `file_id` / `share_id`。

### 2. Histogram Bucket 设计

APIServer 请求快（ms 级），TransferServer 上传慢（最长 300s write_timeout），SSE 长连接（分钟级）。Prometheus 默认 DefBuckets（`0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10`）最长 10s，撑不住三域。

本实现用 `ExponentialBuckets(0.01, 2.5, 13)`，覆盖 `0.01s..~316s`（13 个桶）：
```
0.01, 0.025, 0.0625, 0.156, 0.391, 0.977, 2.44, 6.10, 15.26, 38.15, 95.37, 238.43, ~316s
```
取舍：桶越多精度越高但内存/网络开销越大；13 桶在覆盖三域的同时保持合理开销。

### 3. Exemplar 关联（Metrics ↔ Tracing）

每个 RED 直方图观测点附带 `trace_id`（从 active span context 提取）。实现：
- `exemplarFromContext(ctx)` 从 `trace.SpanContextFromContext(ctx)` 取 `TraceID`
- 无有效 span（未采样 / 未初始化 tracing）时返回 `nil`，退化为普通 Observe
- `ObserveWithExemplar(duration, exemplar)` 在有 trace 时附 exemplar
- `/metrics` 端点启用 `EnableOpenMetrics: true`——OpenMetrics 是唯一可传 exemplar 的格式

Grafana 数据源配置 `exemplarTraceIdDestinations: [{name: trace_id, datasourceUid: tempo}]`，实现点 exemplar → 跳 Tempo trace。

### 4. Scrape-time 自定义 Collector

consumer lag（XPENDING）、emitter dropped 这类信号「查询有成本、变化不频繁」。不用后台 goroutine 轮询，而是实现 `prometheus.Collector` 接口在 `/metrics` 被抓取时现查——这是处理「拉模型下的派生指标」的标准范式。

`InfraCollector` 实现 `Describe`/`Collect`，在 Collect 时：
- `Emitter.EmittedCount()` / `DroppedCount()`：原子读，零成本
- `Consumer.PendingLength(stream)`：XPENDING Redis 往返，3s 超时
- `LogAggregator.DroppedCount()`：原子读

nil 的 source 跳过对应指标族（Redis/PG 不可用降级）。

### 5. 全局单例 + nil 安全

与 `logger.L` / `tracing.Tracer()` 一致：`metrics.Init` 设包级 `defaultCollector`，handler 调 `metrics.Default().Xxx.WithLabelValues(...).Inc()`。Init 前返回 `noopCollector`（仪器有效但不被任何 registry 抓取，Inc 不 panic）。

## 三、指标清单

### RED（基础设施层）

| 指标 | 类型 | label | 说明 |
|------|------|-------|------|
| `nimbus_http_requests_total` | Counter | service, method, route, status_class | HTTP 请求总数 |
| `nimbus_http_request_duration_seconds` | Histogram | service, method, route, status_class | HTTP 请求延迟（秒），跨域 bucket |
| `nimbus_http_requests_in_flight` | Gauge | service, method, route | 处理中请求数 |

`service`: `api` | `transfer`
`status_class`: `2xx` | `3xx` | `4xx` | `5xx` | `1xx`
`route`: 路由模板（`c.FullPath()`），无匹配回退 `/unmatched`

### 业务层

| 指标 | 类型 | label | 说明 |
|------|------|-------|------|
| `nimbus_uploads_completed_total` | Counter | type, result | 上传完成数 |
| `nimbus_upload_bytes_total` | Counter | type | 上传字节数 |
| `nimbus_upload_active_sessions` | Gauge | — | 活跃上传会话数 |
| `nimbus_upload_chunks_received_total` | Counter | — | 接收的分块数 |
| `nimbus_upload_sessions_created_total` | Counter | type | 创建的上传会话数 |
| `nimbus_upload_cancellations_total` | Counter | — | 取消的上传会话数 |
| `nimbus_downloads_total` | Counter | mode, result | 下载次数 |
| `nimbus_download_bytes_total` | Counter | mode | 下载字节数（presign/share 近似） |
| `nimbus_shares_created_total` | Counter | has_password | 分享创建数 |
| `nimbus_share_access_total` | Counter | result | 分享访问次数 |
| `nimbus_user_registrations_total` | Counter | result | 用户注册数 |
| `nimbus_login_attempts_total` | Counter | result | 登录尝试数 |
| `nimbus_quota_monthly_reset_total` | Counter | result | 月度配额重置次数 |
| `nimbus_admin_quota_reset_all_total` | Counter | — | 管理员批量重置配额次数 |
| `nimbus_sse_active_connections` | Gauge | — | 活跃 SSE 连接数 |
| `nimbus_sse_events_pushed_total` | Counter | — | SSE 推送事件数 |

label 值域：
- `type`: `instant` | `multipart`
- `result`: `success` | `error` | `quota_exceeded` | `conflict`（注册）/ `invalid`（分享下载）/ `user_not_found` | `bad_password` | `disabled`（登录）/ `password_wrong` | `exhausted`（分享访问）
- `mode`: `presign` | `stream` | `share`
- `has_password`: `true` | `false`

### infra 层

| 指标 | 类型 | label | 说明 |
|------|------|-------|------|
| `eventbus_emitter_emitted_total` | Counter | — | 成功 XADD 的事件数 |
| `eventbus_emitter_dropped_total` | Counter | — | 丢弃的事件数（背压/编码/XADD 失败） |
| `eventbus_consumer_pending_messages` | Gauge | stream | PEL 长度（consumer lag） |
| `eventbus_consumer_dlq_total` | Counter | — | 移入死信队列的消息数 |
| `eventbus_consumer_messages_processed_total` | Counter | — | 成功处理（ACK）的消息数 |
| `eventbus_consumer_message_processing_errors_total` | Counter | — | 处理失败的消息数 |
| `admin_log_dropped_total` | Counter | — | 丢弃的日志条数 |

## 四、配置

```yaml
observability:
  exporter: stdout          # trace 导出（stdout/otlp/none）
  otlp_endpoint: localhost:4317
  service_name: ""
  sample_ratio: 1.0
  metrics_enabled: true      # 是否启用 Prometheus 指标 + /metrics 端点
  metrics_path: /metrics     # 抓取路径
```

`metrics_enabled=false` 时 `/metrics` 不挂载，`Default()` 返回 noop（不 panic）。

## 五、/metrics 端点暴露策略

- 挂载在 root（与 `/healthz` 同级），**无 JWT 保护**——Prometheus 标准约定
- 生产用反向代理 / 网络隔离保护（如 Nginx 限制内网访问）
- `EnableOpenMetrics: true`：Prometheus 2.5+ 优先协商 OpenMetrics 格式（exemplar 仅此格式可传）

## 六、Grafana 栈部署

见 `deploy/observability/`：
```
docker compose up -d   # 需先 docker compose pull（用户网络可用时）
```

服务端口：
- Prometheus: http://localhost:9090
- Loki: http://localhost:3100
- Tempo: http://localhost:3200（OTLP gRPC :4317）
- Grafana: http://localhost:3000（admin/admin）

三支柱联动配置（自动 provision）：
- Prometheus exemplar `trace_id` → Tempo（点 exemplar 跳 trace）
- Tempo trace → Loki（按 `trace_id` 查日志）
- 切 `observability.exporter=otlp` 即把 trace 送进 Tempo，激活完整闭环

## 七、依赖方向

```
handler → metrics（业务埋点）
middleware → metrics（RED 中间件）
metrics ← eventbus/adminstore（InfraSources 接口，main 注入，避免 metrics→eventbus 反向依赖）
```

`InfraSources` 用接口抽象（`EmitterMetrics`/`ConsumerMetrics`/`LogAggregatorMetrics`），`*eventbus.Emitter` 和 `*eventbus.Consumer` 隐式实现。main 构造 `InfraSources` 注入到 `Collector.RegisterInfra`。

## 八、验证

- `GOARCH=amd64 go vet ./...` + `go build ./...` + `go test ./...` 通过
- `curl localhost:8080/metrics` 含 `nimbus_http_requests_total`、`nimbus_uploads_completed_total`、`eventbus_emitter_dropped_total`、exemplar `trace_id` 行（OpenMetrics）
- `metrics_enabled=false` 时 `/metrics` 不挂载、`Default()` 返回 no-op、不 panic
- `docker compose config` 校验通过
