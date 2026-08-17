package consumers

import (
	"context"
	"fmt"
	"time"

	"github.com/Demetrius2107/NimbusDrive/internal/domain"
	"github.com/Demetrius2107/NimbusDrive/internal/eventbus"
	"github.com/Demetrius2107/NimbusDrive/internal/logger"
	"github.com/go-redis/redis/v8"
	"github.com/jmoiron/sqlx"
	"go.uber.org/zap"
)

// QuotaReconcileConsumer 消费 file.uploaded 事件，异步对账用户配额。
// 比对 users.used_storage 与实际 completed 文件 size 之和，不一致则 warn。
//
// 限频：每用户每小时只对账一次（Redis SETNX 锁），避免高频上传时重复对账。
type QuotaReconcileConsumer struct {
	db     *sqlx.DB
	client redisClient
}

// redisClient 抽象最小所需 Redis 方法（便于测试 mock）。
type redisClient interface {
	SetNX(ctx context.Context, key string, value interface{}, expiration time.Duration) *redis.BoolCmd
}

// NewQuotaReconcileConsumer 构造。db 或 client 为 nil 时 handler 为 no-op。
func NewQuotaReconcileConsumer(db *sqlx.DB, client redisClient) *QuotaReconcileConsumer {
	return &QuotaReconcileConsumer{db: db, client: client}
}

func (q *QuotaReconcileConsumer) Streams(prefix string) []string {
	return []string{prefix + domain.EventFileUploaded}
}

func (q *QuotaReconcileConsumer) Handler() eventbus.Handler {
	return func(ctx context.Context, evt *domain.Event) error {
		if q.db == nil || q.client == nil {
			return nil
		}
		if evt.Type != domain.EventFileUploaded {
			return nil
		}
		userID := evt.ActorID
		if userID == 0 {
			return nil
		}

		// 限频锁：每用户每小时一次
		lockKey := fmt.Sprintf("nimbus:reconcile:lock:%d", userID)
		ok, err := q.client.SetNX(ctx, lockKey, "1", time.Hour).Result()
		if err != nil {
			logger.L.Debug("reconcile lock failed, skip",
				zap.Int64("user_id", userID),
				zap.Error(err),
			)
			return nil // 锁失败不阻塞，本次跳过
		}
		if !ok {
			return nil // 已有锁，本小时已对账过
		}

		q.reconcile(ctx, userID)
		return nil
	}
}

// reconcile 执行实际对账查询。
func (q *QuotaReconcileConsumer) reconcile(ctx context.Context, userID int64) {
	const sumQuery = `SELECT COALESCE(SUM(size), 0) FROM files WHERE user_id = $1 AND status = 'completed' AND deleted_at IS NULL`
	const usedQuery = `SELECT used_storage FROM users WHERE id = $1`

	var actualSize int64
	if err := q.db.GetContext(ctx, &actualSize, sumQuery, userID); err != nil {
		logger.L.Warn("reconcile: query sum(size) failed",
			zap.Int64("user_id", userID),
			zap.Error(err),
		)
		return
	}

	var usedStorage int64
	if err := q.db.GetContext(ctx, &usedStorage, usedQuery, userID); err != nil {
		logger.L.Warn("reconcile: query used_storage failed",
			zap.Int64("user_id", userID),
			zap.Error(err),
		)
		return
	}

	if actualSize != usedStorage {
		logger.L.Warn("quota mismatch detected",
			zap.Int64("user_id", userID),
			zap.Int64("actual_size", actualSize),
			zap.Int64("used_storage", usedStorage),
			zap.Int64("diff", actualSize-usedStorage),
		)
		return
	}
	logger.L.Debug("quota reconcile ok",
		zap.Int64("user_id", userID),
		zap.Int64("size", actualSize),
	)
}

var _ ConsumerBuilder = (*QuotaReconcileConsumer)(nil)
