// 领域事件定义。事件由业务操作触发，经事件总线（Redis Streams）异步投递给消费者。
// 事件类型与载荷契约见 docs/feat-async-events.md。

package domain

import "time"

// Event 是领域事件的统一载体。所有业务事件都封装为 Event，
// 经 Emitter 写入 Redis Streams，由 Consumer 反序列化后分发到具体 handler。
type Event struct {
	// ID 事件唯一标识（UUID），用于消费者侧幂等去重。
	ID string `json:"id"`
	// Type 事件类型，如 EventFileUploaded。消费者按 Type 路由到 handler。
	Type string `json:"type"`
	// OccurredAt 事件发生时间（业务时间，非投递时间）。
	OccurredAt time.Time `json:"occurred_at"`
	// ActorID 触发事件的用户 ID（公开端点如 share.validate 可能为 0）。
	ActorID int64 `json:"actor_id"`
	// Payload 类型化载荷，各事件自定义字段。值须为可 JSON 序列化类型。
	Payload map[string]any `json:"payload"`
	// TraceContext W3C trace context（traceparent+tracestate），跨 Redis Streams 传播追踪上下文。
	// Emitter 在 Emit 时从 ctx 提取注入；Consumer 解码后提取，起 consumer span 续接 trace。
	// 无 active span 时为 nil（omitempty，不占 Stream 字段）。
	TraceContext map[string]string `json:"trace_context,omitempty"`
}

// 事件类型常量。命名约定：{聚合}.{动作}，点分。
const (
	// EventFileUploaded 文件上传完成（含分块合并与秒传两种路径，payload.instant 区分）。
	EventFileUploaded = "file.uploaded"
	// EventShareCreated 分享创建。
	EventShareCreated = "share.created"
	// EventShareAccessed 分享被访问（密码校验通过）。
	EventShareAccessed = "share.accessed"
	// EventUserRegistered 用户注册成功。
	EventUserRegistered = "user.registered"
)
