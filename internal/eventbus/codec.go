// Package eventbus 提供 Redis Streams 事件总线：Emitter（生产者）与 Consumer（消费者）。
//
// 设计目标：把业务事件从同步副作用解耦为异步消息。生产者非阻塞 Emit（背压丢弃），
// 消费者按消费者组订阅，保证至少一次投递 + 幂等 + 毒丸隔离（DLQ）+ 优雅关闭。
package eventbus

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/Demetrius2107/NimbusDrive/internal/domain"
)

// streamFieldKey 是 XADD Values 中存放事件 JSON 的字段名。
// 其余字段（id/type/occurred_at/actor_id）作为独立字段冗余存放，
// 便于 XREADGROUP 后无需反序列化即可按 type 路由。
const (
	streamFieldID           = "id"
	streamFieldType         = "type"
	streamFieldOccurredAt   = "occurred_at"
	streamFieldActorID      = "actor_id"
	streamFieldPayload      = "payload"
	streamFieldTraceContext = "trace_context"
)

// encodeEvent 把 Event 编码为 XADD 的 Values（map[string]interface{}，值均为字符串）。
// payload 和 trace_context 整体 JSON 序列化后存放，消费者解码后按 type 还原。
func encodeEvent(evt *domain.Event) (map[string]interface{}, error) {
	if evt == nil {
		return nil, fmt.Errorf("nil event")
	}
	payloadBytes, err := json.Marshal(evt.Payload)
	if err != nil {
		return nil, fmt.Errorf("marshal payload: %w", err)
	}
	values := map[string]interface{}{
		streamFieldID:         evt.ID,
		streamFieldType:       evt.Type,
		streamFieldOccurredAt: evt.OccurredAt.UTC().Format(time.RFC3339Nano),
		streamFieldActorID:    fmt.Sprintf("%d", evt.ActorID),
		streamFieldPayload:    string(payloadBytes),
	}
	// trace_context 可选：无追踪上下文时不写字段，省 Stream 空间。
	if len(evt.TraceContext) > 0 {
		tcBytes, err := json.Marshal(evt.TraceContext)
		if err != nil {
			return nil, fmt.Errorf("marshal trace_context: %w", err)
		}
		values[streamFieldTraceContext] = string(tcBytes)
	}
	return values, nil
}

// decodeEvent 从 XREADGROUP 返回的消息 Values 还原 Event。
func decodeEvent(values map[string]interface{}) (*domain.Event, error) {
	get := func(k string) string {
		if v, ok := values[k]; ok {
			if s, ok := v.(string); ok {
				return s
			}
		}
		return ""
	}

	occurredAt, err := time.Parse(time.RFC3339Nano, get(streamFieldOccurredAt))
	if err != nil {
		return nil, fmt.Errorf("parse occurred_at: %w", err)
	}

	var actorID int64
	if s := get(streamFieldActorID); s != "" {
		if _, err := fmt.Sscanf(s, "%d", &actorID); err != nil {
			return nil, fmt.Errorf("parse actor_id: %w", err)
		}
	}

	var payload map[string]any
	if s := get(streamFieldPayload); s != "" {
		if err := json.Unmarshal([]byte(s), &payload); err != nil {
			return nil, fmt.Errorf("unmarshal payload: %w", err)
		}
	}

	// trace_context 可选：旧消息无此字段，跳过不影响。
	var traceCtx map[string]string
	if s := get(streamFieldTraceContext); s != "" {
		if err := json.Unmarshal([]byte(s), &traceCtx); err != nil {
			return nil, fmt.Errorf("unmarshal trace_context: %w", err)
		}
	}

	return &domain.Event{
		ID:           get(streamFieldID),
		Type:         get(streamFieldType),
		OccurredAt:   occurredAt,
		ActorID:      actorID,
		Payload:      payload,
		TraceContext: traceCtx,
	}, nil
}
