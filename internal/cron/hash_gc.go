package cron

import (
	"context"
	"time"

	"github.com/Demetrius2107/NimbusDrive/internal/domain"
	"github.com/Demetrius2107/NimbusDrive/internal/logger"
	"github.com/Demetrius2107/NimbusDrive/internal/metrics"
	"github.com/Demetrius2107/NimbusDrive/internal/storage"
	"github.com/jmoiron/sqlx"
	"go.uber.org/zap"
)

// hashLister 抽象 FileHashRepo.ListZeroRef（GC 候选扫描）。
type hashLister interface {
	ListZeroRef(ctx context.Context, ext sqlx.ExtContext, before time.Time, limit int) ([]domain.FileHash, error)
}

// hashDeleter 抽象 FileHashRepo.DeleteIfZeroRef（GC 删对象后删行）。
type hashDeleter interface {
	DeleteIfZeroRef(ctx context.Context, ext sqlx.ExtContext, hash string) (bool, error)
}

// objectRemover 抽象 MinIO.RemoveObject（物理对象删除）。便于单测注入 fake。
type objectRemover interface {
	RemoveObject(ctx context.Context, objectKey string) error
}

// HashGCCron 零引用哈希物理对象 GC 定时任务。
//
// 引用计数 GC 的可达性设计（深挖点 #1）：
// file_hashes.ref_count=0 的行带 zero_ref_at 墓碑，cron 扫墓碑早于 grace 的候选，
// 删 MinIO 对象后删行。grace 窗口（默认 24h）防并发 re-reference 误删。
//
// 安全删除序（防残存竞态）：
//  1. ListZeroRef(now()-grace, batch) 取候选（墓碑已过 grace）
//  2. mc.RemoveObject(storage.ObjectKey(hash)) 删 MinIO 对象
//  3. DeleteIfZeroRef(hash) 再校验 ref_count=0 后删行
//
// 竞态分析：步骤 1→3 间若 Upsert 重新引用该 hash，步骤 3 的 DELETE WHERE
// ref_count=0 不影响行（affected=0），调用方据此跳过——对象已被 re-reference，
// 不删行。MinIO 对象此时已被步骤 2 删除，但 Upsert 不会重建对象（它只改
// ref_count/storage_path），故 re-reference 会指向已删对象。这是 grace 窗口外
// 的残存竞态：用户删除内容→等 24h+→在 GC 执行的秒级窗口内重传同哈希。概率
// 可忽略，以长 grace + 监控指标兜底（生产 dedup 存储的务实取舍）。
//
// 失败不阻塞：单个对象 RemoveObject 失败记指标+日志，行不删（下轮重试）。
type HashGCCron struct {
	hashes    hashLister
	deleter   hashDeleter
	mc        objectRemover
	db        *sqlx.DB
	grace     time.Duration
	batchSize int
	interval  time.Duration
}

// NewHashGCCron 构造。hashes 同时实现 hashLister + hashDeleter（*store.FileHashRepo）。
// 任一依赖为 nil 时 Start 不启动（降级）。
func NewHashGCCron(hashes fileHashGCRepo, mc *storage.MinIO, db *sqlx.DB, graceHours, batchSize, intervalSec int) *HashGCCron {
	return &HashGCCron{
		hashes:    hashes,
		deleter:   hashes,
		mc:        mc,
		db:        db,
		grace:     time.Duration(graceHours) * time.Hour,
		batchSize: batchSize,
		interval:  time.Duration(intervalSec) * time.Second,
	}
}

// fileHashGCRepo 是 *store.FileHashRepo 满足的复合接口（ListZeroRef + DeleteIfZeroRef）。
type fileHashGCRepo interface {
	hashLister
	hashDeleter
}

// Start 启动后台 goroutine，返回停止函数。依赖为 nil 直接返回空停止函数。
func (c *HashGCCron) Start(ctx context.Context) func() {
	if c.hashes == nil || c.deleter == nil || c.mc == nil || c.db == nil {
		logger.L.Warn("hash gc cron disabled: dependency nil")
		return func() {}
	}
	stop := make(chan struct{})
	go func() {
		defer close(stop)
		c.tick(ctx)
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

// tick 执行一次 GC 批次。
func (c *HashGCCron) tick(ctx context.Context) {
	before := time.Now().Add(-c.grace)
	candidates, err := c.hashes.ListZeroRef(ctx, c.db, before, c.batchSize)
	if err != nil {
		logger.L.Error("hash gc: list zero-ref failed", zap.Error(err))
		return
	}
	if len(candidates) == 0 {
		return
	}

	var reclaimed, failed int
	for _, h := range candidates {
		// 1. 删 MinIO 物理对象。
		objectKey := storage.ObjectKey(h.HashSHA256)
		if err := c.mc.RemoveObject(ctx, objectKey); err != nil {
			metrics.Default().HashGCObjectsFailed.WithLabelValues("remove_failed").Inc()
			logger.L.Warn("hash gc: remove object failed, skipping row delete",
				zap.String("hash", h.HashSHA256), zap.String("object_key", objectKey), zap.Error(err))
			failed++
			continue // 行不删，下轮重试删对象
		}
		// 2. 再校验 ref_count=0 后删行（防 grace 窗口外残存竞态）。
		deleted, err := c.deleter.DeleteIfZeroRef(ctx, c.db, h.HashSHA256)
		if err != nil {
			metrics.Default().HashGCObjectsFailed.WithLabelValues("delete_failed").Inc()
			logger.L.Error("hash gc: delete row failed",
				zap.String("hash", h.HashSHA256), zap.Error(err))
			failed++
			continue
		}
		if !deleted {
			// 行已被 Upsert 重新引用（ref_count>0）——对象已删但行保留。
			// 残存竞态：re-reference 指向已删对象，需上层重建。记指标监控。
			metrics.Default().HashGCObjectsFailed.WithLabelValues("race_re_referenced").Inc()
			logger.L.Warn("hash gc: row re-referenced after object delete (race)",
				zap.String("hash", h.HashSHA256))
			continue
		}
		reclaimed++
	}
	metrics.Default().HashGCObjectsReclaimed.Add(float64(reclaimed))
	logger.L.Info("hash gc tick done",
		zap.Int("scanned", len(candidates)),
		zap.Int("reclaimed", reclaimed), zap.Int("failed", failed))
}
