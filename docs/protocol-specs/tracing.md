# 分布式追踪协议

> OpenTelemetry trace context 跨服务传播约定。
> 对应分支：feat/observability-tracing。

## 概述

NimbusDrive 双服务（Gin APIServer + Hertz TransferServer）+ Redis Streams 事件总线 + 客户端中介的跨服务流程，用 OpenTelemetry W3C TraceContext 把追踪上下文贯穿所有边界。每条日志带 `trace_id`/`span_id`，可在日志中按 trace 关联完整调用链。

### 传播格式

W3C `traceparent` 头：`00-<trace_id>-<span_id>-<flags>`

- `trace_id`：32 hex（16 字节），同一 trace 内所有 span 共享
- `span_id`：16 hex（8 字节），每个 span 唯一
- `flags`：2 hex，`01` = sampled

全局 propagator = `TraceContext` + `Baggage`。

## 四个传播边界

### 1. HTTP 边界（Gin/Hertz 入站）

`GinTracer` / `HertzTracer` 中间件：
- 入站：从请求头提取 `traceparent`（上游传入则续接，否则起根 span）
- 起 `SpanKindServer` span，operationName = 路由模板（`/api/v1/files/:id`，避免高基数）
- 注入 `c.Request.Context()`，后续 handler/中间件读到
- 出站：记录 HTTP status 到 span，`span.End()`

中间件链顺序：`RequestID → Tracer → Logger → Recovery`（Logger 用 `FromContext` 读 trace_id）。

### 2. 异步边界（Redis Streams 事件总线）

**生产者（Emitter）**：
- `Emit(ctx, evt)` 从 ctx 提取 W3C trace context 注入 `evt.TraceContext`（`map[string]string`，含 traceparent + tracestate）
- codec.go 把 `TraceContext` JSON 序列化为独立 Stream 字段 `trace_context`
- 无 active span 时 `TraceContext` 为 nil，omitempty 不写字段（向后兼容旧消息）

**消费者（Consumer）**：
- `handleMessage` 解码后 `tracing.Extract(ctx, evt.TraceContext)` 恢复追踪上下文
- 起 `SpanKindConsumer` span（`consume.<event_type>`），作为生产者 span 的 follow-from child
- handler 收到的 ctx 带 trace，日志自动关联 trace_id

链路示例：TransferServer 上传完成 → emit `file.uploaded`（携 traceparent）→ APIServer QuotaConsumer 起 consumer span（同一 trace）→ 日志带相同 trace_id。

### 3. 客户端中介边界（分享下载）

分享下载是两跳独立 HTTP 请求，客户端中介：
1. APIServer `ValidateShare` → `issueDownloadToken`：从 ctx 提取 `traceparent` 存入 `ShareDownloadToken.TraceParent`
2. 客户端拿 token 发第二个请求到 TransferServer
3. TransferServer `Redeem`：`tracing.ExtractFromTraceParent(ctx, tok.TraceParent)` → 起 `SpanKindInternal` span

令牌是跨客户端中介边界的唯一 trace 载体。

### 4. pub/sub 边界（配额 SSE）

- `Notifier.NotifyChange` 从 ctx 提取 `traceparent` 注入 `QuotaChangeEvent.TraceParent`
- 事件 PUBLISH 到 pub/sub + XADD 到 Stream（两处都带 TraceParent）
- SSE handler 每条 pub/sub 消息起 `SpanKindConsumer` linked span（`SSE.push.<type>`）
- **不为整条 SSE 连接起 span**：连接多路复用 N 个管理员变更，每条消息独立 span

链路示例：管理员 `POST /admin/users/quota/reset` → NotifyChange 注入 traceparent → PUBLISH → SSE handler linked span → 推给客户端。

## 日志关联

`logger.FromContext(ctx)` 从 active span 读 SpanContext，返回带 `trace_id`/`span_id` 字段的 zap logger。所有中间件和业务 handler 应优先用 `FromContext(ctx)` 替代 `logger.L`。

```json
{"ts":"2026-08-19T...","level":"INFO","service":"api","trace_id":"4bf92f3577b34da6a3ce929d0e0e4736","span_id":"00f067aa0ba902b7","msg":"http","method":"GET","path":"/api/v1/files","status":200}
```

## 配置

`config.dev.yaml` 的 `observability` 段：

```yaml
observability:
  exporter: stdout        # stdout（写 stderr，默认零依赖）| otlp（gRPC 推 collector）| none（no-op）
  otlp_endpoint: localhost:4317
  service_name: ""        # 空=按二进制名 api/transfer
  sample_ratio: 1.0       # 0-1，1=全采样
```

切换到 OTLP（如 Jaeger/Tempo）：改 `exporter: otlp`，起本地 collector，不改代码。

### 采样策略

`ParentBased(TraceIDRatioBased(ratio))`：
- 根 span 按 `sample_ratio` 采样
- 子 span 跟随父决策（已采样 trace 全采，未采样全不采）
- 开发期 `1.0` 全采样；生产调低

## 导出器

| Exporter | 行为 | 适用 |
|----------|------|------|
| `stdout` | pretty-print 到 stderr | 开发期默认，零外部依赖 |
| `otlp` | OTLP gRPC 推 collector | 生产，接 Jaeger/Tempo |
| `none` | no-op，不导出不采样 | 降级/关闭 tracing |

OTLP 对 localhost 端点自动用 insecure（无 TLS）；生产端点应配 TLS。

## 降级

- `tracing.Init` 失败：告警并继续启动，tracing 为 no-op（不影响业务）
- `exporter=none`：TracerProvider 不导出，所有 span 操作为空操作
- 无 active span 的 ctx：`Inject` 返回 nil，`TraceParentString` 返回空串，omitempty 不占字段
