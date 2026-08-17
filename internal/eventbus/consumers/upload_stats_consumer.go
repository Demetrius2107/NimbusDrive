package consumers

import (
	"context"
	"fmt"
	"time"

	"github.com/Demetrius2107/NimbusDrive/internal/domain"
	"github.com/Demetrius2107/NimbusDrive/internal/eventbus"
	"github.com/Demetrius2107/NimbusDrive/internal/logger"
	"github.com/go-redis/redis/v8"
	"go.uber.org/zap"
)

// UploadStatsConsumer 消费 file.uploaded 事件，累计用户当日上传字节/文件数。
// 数据写入 Redis（nimbus:stats:upload:{user}:{date}:bytes / :files），
// 管理端后续可查。幂等由 Consumer 框架的 SETNX 机制保证。
type UploadStatsConsumer struct {
	client *redis.Client
}

// NewUploadStatsConsumer 构造。client 为 nil 时 handler 为 no-op。
func NewUploadStatsConsumer(client *redis.Client) *UploadStatsConsumer {
	return &UploadStatsConsumer{client: client}
}

func (u *UploadStatsConsumer) Streams(prefix string) []string {
	return []string{prefix + domain.EventFileUploaded}
}

func (u *UploadStatsConsumer) Handler() eventbus.Handler {
	return func(ctx context.Context, evt *domain.Event) error {
		if u.client == nil {
			return nil
		}
		if evt.Type != domain.EventFileUploaded {
			return nil
		}
		size, _ := evt.Payload["size"].(float64)
		userID := evt.ActorID
		if userID == 0 {
			return nil
		}
		date := evt.OccurredAt.UTC().Format("2006-01-02")
		bytesKey := fmt.Sprintf("nimbus:stats:upload:%d:%s:bytes", userID, date)
		filesKey := fmt.Sprintf("nimbus:stats:upload:%d:%s:files", userID, date)

		// 当日统计键设 7 天 TTL，过期自动清理
		pipe := u.client.TxPipeline()
		pipe.IncrBy(ctx, bytesKey, int64(size))
		pipe.Incr(ctx, filesKey)
		pipe.Expire(ctx, bytesKey, 7*24*time.Hour)
		pipe.Expire(ctx, filesKey, 7*24*time.Hour)
		if _, err := pipe.Exec(ctx); err != nil {
			logger.L.Warn("upload stats incr failed",
				zap.Int64("user_id", userID),
				zap.String("event_id", evt.ID),
				zap.Error(err),
			)
			return err // 返回 error 触发重试（框架保证幂等）
		}
		return nil
	}
}

var _ ConsumerBuilder = (*UploadStatsConsumer)(nil)
