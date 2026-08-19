package consumers

import (
	"context"
	"time"

	"github.com/Demetrius2107/NimbusDrive/internal/domain"
	"github.com/Demetrius2107/NimbusDrive/internal/eventbus"
	"github.com/Demetrius2107/NimbusDrive/internal/logger"
	"github.com/jmoiron/sqlx"
	"go.uber.org/zap"
)

// currentPeriodUTC 返回当前 UTC 月份的 "YYYY-MM" 格式。
func currentPeriodUTC() string {
	return time.Now().UTC().Format("2006-01")
}

// uploadIncrer 抽象 QuotaRepo.IncrUpload，便于单测 mock。
type uploadIncrer interface {
	IncrUpload(ctx context.Context, userID, delta int64) error
}

// QuotaConsumer 消费 file.uploaded 事件，累计当月上传字节到 quota_periods。
// 与 UploadStatsConsumer（Redis 临时统计 7 天 TTL）互补：
// Stats 是热统计，QuotaPeriods 是 PG 持久配额（月度重置）。
//
// CheckHash 前置只检查不扣，真正的扣减由本消费者完成（避免预扣回补复杂度）。
// 幂等由 Consumer 框架的 SETNX 机制保证。
type QuotaConsumer struct {
	quotas uploadIncrer
}

// NewQuotaConsumer 构造。quotas 为 nil 时 handler 为 no-op。
func NewQuotaConsumer(db *sqlx.DB) *QuotaConsumer {
	return &QuotaConsumer{quotas: &quotaIncrerAdapter{db: db}}
}

// quotaIncrerAdapter 适配 *sqlx.DB → uploadIncrer（内部用 QuotaRepo）。
type quotaIncrerAdapter struct{ db *sqlx.DB }

func (a *quotaIncrerAdapter) IncrUpload(ctx context.Context, userID, delta int64) error {
	// 直接用 store.QuotaRepo，避免循环导入 store → consumers。
	// 这里用内联 SQL 保持与 QuotaRepo.IncrUpload 一致。
	period := currentPeriodUTC()
	res, err := a.db.ExecContext(ctx, `
		UPDATE quota_periods
		SET upload_bytes = upload_bytes + $2
		WHERE user_id = $1 AND period = $3
		  AND (upload_quota = 0 OR upload_bytes + $2 <= upload_quota)`,
		userID, delta, period)
	if err != nil {
		return err
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		return domain.ErrQuotaExceeded
	}
	return nil
}

func (q *QuotaConsumer) Streams(prefix string) []string {
	return []string{prefix + domain.EventFileUploaded}
}

func (q *QuotaConsumer) Handler() eventbus.Handler {
	return func(ctx context.Context, evt *domain.Event) error {
		if q.quotas == nil {
			return nil
		}
		if evt.Type != domain.EventFileUploaded {
			return nil
		}
		size, _ := evt.Payload["size"].(float64)
		userID := evt.ActorID
		if userID == 0 || size <= 0 {
			return nil
		}
		if err := q.quotas.IncrUpload(ctx, userID, int64(size)); err != nil {
			logger.L.Warn("quota consumer incr upload failed",
				zap.Int64("user_id", userID),
				zap.String("event_id", evt.ID),
				zap.Error(err),
			)
			return err // 返回 error 触发重试（框架保证幂等）
		}
		return nil
	}
}

var _ ConsumerBuilder = (*QuotaConsumer)(nil)
