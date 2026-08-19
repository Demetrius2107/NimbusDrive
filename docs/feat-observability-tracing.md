# feat/observability-tracing 计划

> 分支：`feat/observability-tracing`，从 master（含 feat/quota-streaming 合入 1392c19）迁出。
> 状态：进行中

## 深挖点

NimbusDrive 双服务（Gin APIServer + Hertz TransferServer）+ Redis Streams 事件总线 + 客户端中介的跨服务流程，目前只有孤立的 per-request `X-Request-ID`，跨服务/跨异步边界追踪链完全断裂。本分支用 OpenTelemetry 把追踪上下文贯穿所有边界：

1. **HTTP 边界**：W3C `traceparent` 头在 Gin/Hertz 入站提取 + 出站注入
2. **异步边界**（Redis Streams 事件总线）：trace context 编码进 `domain.Event` + codec 字段，Emitter 注入 / Consumer 提取并起 consumer span
3. **客户端中介边界**（分享下载）：`ShareDownloadToken` 携带 trace context，APIServer 签发时注入、TransferServer 兑换时提取
4. **pub/sub 边界**（配额 SSE）：`QuotaChangeEvent` 携带 trace context，Notifier 注入、SSE handler 提取起 linked span

这套 propagation 模式是分布式追踪的核心工程——context carrier 选择、inject/extract 对称性、跨进程边界保持 span 亲子关系。换 Kafka/RabbitMQ/gRPC 一条不少。

## 决策

1. **OpenTelemetry SDK + stdout exporter（默认）**，可配置切 OTLP gRPC，改配置不改代码
2. **W3C TraceContext 传播**：`traceparent` 头为标准载体
3. **不替换 X-Request-ID**：保留现有 request_id，trace_id 作为新维度叠加；request_id 进 baggage 便于日志关联
4. **Event 携带 trace context**：`domain.Event` 加 `TraceContext map[string]string`；codec 编码为独立 Stream 字段；Emitter 在 Emit 时从 context 提取注入；Consumer 解码后提取起 `SpanKindConsumer` span（link 到 producer span）
5. **ShareDownloadToken 携带 `TraceParent string`**：签发注入、兑换提取
6. **QuotaChangeEvent 携带 `TraceParent string`**：Notifier 注入；SSE handler 每条 pub/sub 消息起 linked span（不是连接级 span——连接多路复用）
7. **logger 关联 trace_id**：`logger.FromContext(ctx)` 从 active span 注入 trace_id/span_id 字段
8. **采样**：`ParentBased(TraceIDRatioBased(ratio))`，开发期 1.0 全采样
9. **不引入 Prometheus 指标**（留后续分支）
10. **前端不在范围**：W3C 传播是服务端职责，浏览器无 SDK 时根 span 在入口服务生成

## 范围

### 在范围
- OTel SDK 初始化（TracerProvider + exporter + 采样 + 优雅关闭）
- 配置 `observability` 段（exporter: stdout/otlp/none；otlp_endpoint；service_name；sample_ratio）
- Gin/Hertz trace 中间件（自写薄层，兼容现有 RequestID/Logger 链）
- 日志 trace 关联：`logger.FromContext(ctx)` 注入 trace_id/span_id
- 事件总线 trace 传播：Event.TraceContext + codec + Emit 接受 ctx + Consumer 提取起 span
- 分享下载 trace 传播：ShareDownloadToken.TraceParent
- 配额推送 trace 传播：QuotaChangeEvent.TraceParent + SSE linked span
- 单测 + 文档

### 不在范围
- Prometheus 指标 / /metrics 端点
- 前端 trace SDK
- Jaeger/Tempo 部署（导出端可配置，用户自行起 collector）
- MinIO 对象级追踪（预签名直连无法注入）

## Commit 拆分（预期 8 个）
1. `feat(config): observability 配置段 + config.dev.yaml`
2. `feat(tracing): OTel provider + propagation + traceparent 辅助（stdout/otlp 可配）`
3. `feat(middleware): Gin/Hertz trace 中间件 + logger trace 关联`
4. `feat(eventbus): Event.TraceContext + codec 编解码 + Emit 接受 ctx + Consumer 提取起 span`
5. `feat(handler): 分享下载 token 携带 traceparent（签发注入/兑换提取）`
6. `feat(quota): QuotaChangeEvent 携带 traceparent + SSE handler linked span`
7. `feat(api): 双服务 TracerProvider 启动接入 + trace 中间件挂载`
8. `test+docs: trace 往返单测 + protocol-specs tracing + 设计文档 + 路线图`
