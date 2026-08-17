// Package consumers 实现具体的事件消费者业务逻辑。
// 每个消费者订阅一组事件流，实现特定业务副作用（审计日志、统计、对账等）。
// 消费者由 APIServer 启动时构造并 Start。
package consumers

import (
	"context"
	"encoding/json"
	"strconv"

	"github.com/Demetrius2107/NimbusDrive/internal/adminstore"
	"github.com/Demetrius2107/NimbusDrive/internal/domain"
	"github.com/Demetrius2107/NimbusDrive/internal/eventbus"
	"github.com/Demetrius2107/NimbusDrive/internal/logger"
	"github.com/go-redis/redis/v8"
	"go.uber.org/zap"
)

// AuditConsumer 把业务事件转为操作日志，经 LogAggregator 异步落库。
// 订阅全部事件类型，实现统一审计。
type AuditConsumer struct {
	la *adminstore.LogAggregator
}

// NewAuditConsumer 构造审计消费者。la 为 nil 时 handler 为 no-op。
func NewAuditConsumer(la *adminstore.LogAggregator) *AuditConsumer {
	return &AuditConsumer{la: la}
}

// Streams 返回订阅的 stream 列表（需与 Emitter 的 streamPrefix 拼接）。
func (a *AuditConsumer) Streams(prefix string) []string {
	return []string{
		prefix + domain.EventFileUploaded,
		prefix + domain.EventShareCreated,
		prefix + domain.EventShareAccessed,
		prefix + domain.EventUserRegistered,
	}
}

// Handler 返回事件处理函数。
func (a *AuditConsumer) Handler() eventbus.Handler {
	return func(ctx context.Context, evt *domain.Event) error {
		if a.la == nil {
			return nil
		}
		log := eventToOperationLog(evt)
		if !a.la.Record(log) {
			logger.L.Warn("audit log dropped: aggregator channel full",
				zap.String("event_type", evt.Type),
				zap.String("event_id", evt.ID),
			)
		}
		return nil
	}
}

// eventToOperationLog 把领域事件映射为操作日志记录。
func eventToOperationLog(evt *domain.Event) adminstore.OperationLog {
	detail, _ := json.Marshal(evt.Payload)
	var actorType string = "user"
	var actorID *int64
	if evt.ActorID != 0 {
		id := evt.ActorID
		actorID = &id
	} else {
		actorType = "system" // 公开端点（如 share.accessed）无登录用户
	}

	targetType, targetID := extractTarget(evt)

	return adminstore.OperationLog{
		ActorID:    actorID,
		ActorType:  actorType,
		Action:     "event." + evt.Type,
		TargetType: targetType,
		TargetID:   targetID,
		Detail:     detail,
	}
}

// extractTarget 从事件载荷提取 target_type/target_id（便于按目标筛选日志）。
func extractTarget(evt *domain.Event) (*string, *string) {
	switch evt.Type {
	case domain.EventFileUploaded:
		t := "file"
		if id, ok := evt.Payload["file_id"].(float64); ok {
			s := strconv.FormatInt(int64(id), 10)
			return &t, &s
		}
		return &t, nil
	case domain.EventShareCreated, domain.EventShareAccessed:
		t := "share"
		if id, ok := evt.Payload["share_id"].(string); ok {
			return &t, &id
		}
		return &t, nil
	case domain.EventUserRegistered:
		t := "user"
		if id, ok := evt.Payload["user_id"].(float64); ok {
			s := strconv.FormatInt(int64(id), 10)
			return &t, &s
		}
		return &t, nil
	}
	return nil, nil
}

// Compile-time check: AuditConsumer implements ConsumerBuilder.
var _ ConsumerBuilder = (*AuditConsumer)(nil)

// ConsumerBuilder 由具体消费者实现，提供 Streams + Handler 供启动器组装。
type ConsumerBuilder interface {
	Streams(prefix string) []string
	Handler() eventbus.Handler
}

// Build 构造一个完整 Consumer（供 APIServer 启动时调用）。
func Build(client *redis.Client, cfg Config, builder ConsumerBuilder, consumerName string) *eventbus.Consumer {
	return eventbus.NewConsumer(
		client,
		cfg.ConsumerGroup,
		consumerName,
		builder.Streams(cfg.StreamPrefix),
		builder.Handler(),
		cfg.MaxRetries,
		cfg.BlockMs,
		cfg.DLQPrefix,
	)
}

// Config 是构造消费者所需的配置（从 EventBusConfig 转换）。
type Config struct {
	StreamPrefix  string
	ConsumerGroup string
	MaxRetries    int
	BlockMs       int
	DLQPrefix     string
}
