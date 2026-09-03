package cron

import (
	"context"
	"time"

	"github.com/Demetrius2107/NimbusDrive/internal/domain"
	"github.com/Demetrius2107/NimbusDrive/internal/eventbus"
	"github.com/Demetrius2107/NimbusDrive/internal/logger"
	"github.com/Demetrius2107/NimbusDrive/internal/metrics"
	"github.com/Demetrius2107/NimbusDrive/internal/store"
	"github.com/jmoiron/sqlx"
	"go.uber.org/zap"
)

// outboxFetcher 抽象 OutboxRepo 的 relay 所需能力。
type outboxFetcher interface {
	FetchPending(ctx context.Context, ext sqlx.ExtContext, limit int) ([]store.OutboxMessage, error)
	MarkPublished(ctx context.Context, ext sqlx.ExtContext, id string) error
	IncAttempt(ctx context.Context, ext sqlx.ExtContext, id string) error
	CountPending(ctx context.Context) (int64, error)
}

// eventPublisher 抽象 Emitter.Publish（同步 XAdd）。
type eventPublisher interface {
	Publish(ctx context.Context, evt *domain.Event) error
}

// OutboxRelay 轮询 outbox 表，把未投递消息 XAdd 到 Redis Streams 后回写 published_at。
//
// 解决 dual-write 问题（深挖点 #2）：业务事务与事件写入同一事务，relay 轮询投递。
// at-least-once 投递：XAdd 成功后回写 published_at；若回写前崩溃，下轮重发，
// 消费端已有 Redis SETNX(eventID) 幂等（consumer.go:230）兜底。
//
// 多实例水平扩展：FetchPending 用 FOR UPDATE SKIP LOCKED，各 relay 实例取不同行，
// 无主并发。每条消息独立事务：XAdd 在事务外（Redis 非事务资源），MarkPublished
// 在事务内——XAdd 成功但 MarkPublished 失败时下轮重发（幂等兜底）。
type OutboxRelay struct {
	outbox    outboxFetcher
	emitter   eventPublisher
	db        *sqlx.DB
	batchSize int
	interval  time.Duration
}

// NewOutboxRelay 构造。任一依赖为 nil 时 Start 不启动（降级）。
func NewOutboxRelay(outbox *store.OutboxRepo, emitter *eventbus.Emitter, db *sqlx.DB, batchSize, intervalSec int) *OutboxRelay {
	return &OutboxRelay{
		outbox:    outbox,
		emitter:   emitter,
		db:        db,
		batchSize: batchSize,
		interval:  time.Duration(intervalSec) * time.Second,
	}
}

// Start 启动后台 goroutine，返回停止函数。依赖为 nil 直接返回空停止函数。
func (c *OutboxRelay) Start(ctx context.Context) func() {
	if c.outbox == nil || c.emitter == nil || c.db == nil {
		logger.L.Warn("outbox relay disabled: dependency nil")
		return func() {}
	}
	stop := make(chan struct{})
	go func() {
		defer close(stop)
		c.tick(ctx) // 启动时立即跑一次（覆盖进程重启后积压）
		ticker := time.NewTicker(c.interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				c.tick(ctx)
			}
		}
	}()
	return func() { <-stop }
}

// tick 执行一次投递批次。开事务 → relayBatch → commit。
func (c *OutboxRelay) tick(ctx context.Context) {
	// 开事务取待投递消息（FOR UPDATE SKIP LOCKED）。
	tx, err := c.db.BeginTxx(ctx, nil)
	if err != nil {
		logger.L.Error("outbox relay: begin tx failed", zap.Error(err))
		return
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	published, failed, scanned := c.relayBatch(ctx, tx)

	// 提交事务：MarkPublished 的回写持久化。IncAttempt 的递增也持久化。
	if err := tx.Commit(); err != nil {
		logger.L.Error("outbox relay: commit failed", zap.Error(err))
		return
	}
	committed = true
	if scanned > 0 {
		logger.L.Info("outbox relay tick done",
			zap.Int("scanned", scanned),
			zap.Int("published", published), zap.Int("failed", failed))
	}
}

// relayBatch 在给定 executor（tx 或 db）上执行投递逻辑。返回 published/failed/scanned。
// 与 tick 分离便于单测：测试注入 fake executor + fake outbox + fake publisher。
func (c *OutboxRelay) relayBatch(ctx context.Context, ext sqlx.ExtContext) (published, failed, scanned int) {
	msgs, err := c.outbox.FetchPending(ctx, ext, c.batchSize)
	if err != nil {
		logger.L.Error("outbox relay: fetch pending failed", zap.Error(err))
		return 0, 0, 0
	}
	scanned = len(msgs)
	if scanned == 0 {
		return 0, 0, 0
	}

	for _, msg := range msgs {
		evt, err := msg.ToEvent()
		if err != nil {
			// 解码失败：消息本身有问题，递增 attempt 跳过（避免毒丸阻塞队列）
			metrics.Default().OutboxRelayErrors.WithLabelValues("decode_failed").Inc()
			_ = c.outbox.IncAttempt(ctx, ext, msg.ID)
			failed++
			continue
		}

		// XAdd 到 Redis Streams（事务外资源，不参与 DB 事务）。
		if err := c.emitter.Publish(ctx, evt); err != nil {
			metrics.Default().OutboxRelayErrors.WithLabelValues("publish_failed").Inc()
			_ = c.outbox.IncAttempt(ctx, ext, msg.ID)
			logger.L.Warn("outbox relay: publish failed, will retry next tick",
				zap.String("event_id", msg.ID), zap.String("event_type", msg.EventType), zap.Error(err))
			failed++
			continue
		}

		// XAdd 成功，回写 published_at。
		if err := c.outbox.MarkPublished(ctx, ext, msg.ID); err != nil {
			// XAdd 已成功但回写失败：下轮会重发（at-least-once，消费端幂等兜底）。
			metrics.Default().OutboxRelayErrors.WithLabelValues("mark_failed").Inc()
			logger.L.Error("outbox relay: mark published failed, will redeliver",
				zap.String("event_id", msg.ID), zap.Error(err))
			failed++
			continue
		}
		published++
		metrics.Default().OutboxRelayPublished.Inc()
	}
	return published, failed, scanned
}
