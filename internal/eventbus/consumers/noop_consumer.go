package consumers

import (
	"context"

	"github.com/Demetrius2107/NimbusDrive/internal/domain"
	"github.com/Demetrius2107/NimbusDrive/internal/eventbus"
	"github.com/Demetrius2107/NimbusDrive/internal/logger"
	"go.uber.org/zap"
)

// NoopConsumer 是消费者开发骨架模板。只记录日志，无业务副作用。
// 新增消费者时复制此文件，修改 Streams 与 Handler 实现即可。
type NoopConsumer struct{}

func NewNoopConsumer() *NoopConsumer { return &NoopConsumer{} }

func (n *NoopConsumer) Streams(prefix string) []string {
	// 示例：订阅 file.uploaded。按需改为目标事件类型。
	return []string{prefix + domain.EventFileUploaded}
}

func (n *NoopConsumer) Handler() eventbus.Handler {
	return func(ctx context.Context, evt *domain.Event) error {
		logger.L.Info("noop consumer received event",
			zap.String("event_type", evt.Type),
			zap.String("event_id", evt.ID),
			zap.Int64("actor_id", evt.ActorID),
		)
		return nil
	}
}

var _ ConsumerBuilder = (*NoopConsumer)(nil)
