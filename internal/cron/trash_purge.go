// Package cron 封装 NimbusDrive 的后台清理/relay 定时任务。
//
// 设计：不依赖外部 cron 守护进程，进程内 time.Ticker 驱动。每个 cron 自包含
// struct + Start(ctx) func()（与 quota.MonthlyResetCron 模式一致）。crons 全部
// 跑在 APIServer（控制面），TransferServer 保持纯数据面。
//
// 降级：任一依赖（PG/MinIO/Redis）为 nil 时 Start 返回空停止函数，不 panic。
// 批次：每 tick 处理 batch_size 条，避免单事务扫全表持锁过久。
package cron

import (
	"context"
	"errors"
	"time"

	"github.com/Demetrius2107/NimbusDrive/internal/domain"
	"github.com/Demetrius2107/NimbusDrive/internal/logger"
	"github.com/Demetrius2107/NimbusDrive/internal/metrics"
	"github.com/Demetrius2107/NimbusDrive/internal/store"
	"github.com/jmoiron/sqlx"
	"go.uber.org/zap"
)

// trashPurger 抽象 trash purge 所需的 FileRepo 能力。用接口解耦，便于单测注入 fake。
type trashPurger interface {
	ListExpiredTrash(ctx context.Context, retentionDays, limit int) ([]domain.FileNode, error)
	ListDescendants(ctx context.Context, id int64) ([]domain.FileNode, error)
	HardDeleteRecursive(ctx context.Context, id int64) error
	HardDelete(ctx context.Context, id int64) error
}

// hashDecrer 抽象 FileHashRepo.DecrRef（executor 接口接受 DB 或 Tx）。
type hashDecrer interface {
	DecrRef(ctx context.Context, ext sqlx.ExtContext, hash string) error
}

// quotaRefunder 抽象 UserRepo.IncrUsedStorage（配额回补）。
type quotaRefunder interface {
	IncrUsedStorage(ctx context.Context, ext sqlx.ExtContext, userID, delta int64) error
}

// TrashPurgeCron 回收站 30 天物理清理定时任务。
//
// 设计决策 #1：files.deleted_at 软删除，30 天后物理清理。
// 清理流程（对每个过期节点）：
//  1. ListDescendants 收集后代文件 hash（递归 CTE，一次性算子树）
//  2. 对每个 hash DecrRef（ref_count--，tombstone 化保留行供 hash GC 回收）
//  3. HardDeleteRecursive / HardDelete 删 files 行
//  4. IncrUsedStorage(userID, -size) 配额回补
//
// 批次化：每 tick 处理 batch_size 个顶层节点，避免单事务扫全表持锁过久。
// 失败不阻塞：单个节点清理失败记日志+继续下一个，下轮重试。
type TrashPurgeCron struct {
	files         trashPurger
	hashes        hashDecrer
	users         quotaRefunder
	db            *sqlx.DB
	retentionDays int
	batchSize     int
	interval      time.Duration
}

// NewTrashPurgeCron 构造。任一依赖为 nil 时 Start 不启动（降级）。
func NewTrashPurgeCron(files *store.FileRepo, hashes *store.FileHashRepo, users *store.UserRepo, db *sqlx.DB, retentionDays, batchSize, intervalSec int) *TrashPurgeCron {
	return &TrashPurgeCron{
		files:         files,
		hashes:        hashes,
		users:         users,
		db:            db,
		retentionDays: retentionDays,
		batchSize:     batchSize,
		interval:      time.Duration(intervalSec) * time.Second,
	}
}

// Start 启动后台 goroutine，返回停止函数。ctx 取消时停止。依赖为 nil 直接返回空停止函数。
func (c *TrashPurgeCron) Start(ctx context.Context) func() {
	if c.files == nil || c.hashes == nil || c.users == nil || c.db == nil {
		logger.L.Warn("trash purge cron disabled: dependency nil")
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

// tick 执行一次清理批次。
func (c *TrashPurgeCron) tick(ctx context.Context) {
	nodes, err := c.files.ListExpiredTrash(ctx, c.retentionDays, c.batchSize)
	if err != nil {
		logger.L.Error("trash purge: list expired trash failed", zap.Error(err))
		return
	}
	if len(nodes) == 0 {
		return
	}

	var deleted int
	for _, node := range nodes {
		if err := c.purgeNode(ctx, node); err != nil {
			logger.L.Error("trash purge: node failed, skipping",
				zap.Int64("file_id", node.ID), zap.Error(err))
			continue
		}
		deleted++
	}
	metrics.Default().TrashPurgeFilesDeleted.Add(float64(deleted))
	logger.L.Info("trash purge tick done",
		zap.Int("scanned", len(nodes)), zap.Int("purged", deleted))
}

// purgeNode 清理单个过期节点（含子树）。
func (c *TrashPurgeCron) purgeNode(ctx context.Context, node domain.FileNode) error {
	// 1. 收集后代文件 hash（含已软删除的子节点）。
	descendants, err := c.files.ListDescendants(ctx, node.ID)
	if err != nil {
		return err
	}
	// 2. 对每个物理文件的 hash 做 ref_count--（tombstone 化，保留行供 hash GC 回收）。
	for _, desc := range descendants {
		if desc.HashSHA256 != nil && *desc.HashSHA256 != "" {
			if err := c.hashes.DecrRef(ctx, c.db, *desc.HashSHA256); err != nil && !errors.Is(err, domain.ErrNotFound) {
				logger.L.Warn("trash purge: decr ref failed",
					zap.String("hash", *desc.HashSHA256), zap.Error(err))
			}
		}
	}
	// 3. 物理删除 files 行（递归 CTE 一次性删子树）。
	if node.IsFolder {
		err = c.files.HardDeleteRecursive(ctx, node.ID)
	} else {
		err = c.files.HardDelete(ctx, node.ID)
	}
	if err != nil && !errors.Is(err, domain.ErrNotFound) {
		return err
	}
	// 4. 配额回补。node.Size 是该节点自身大小；文件夹后代 size 已在递归删除中处理，
	// 但 used_storage 按各节点 size 累加，故仅回补当前节点 size（后代回补由各自的
	// ListExpiredTrash 入选时独立处理——顶层文件夹入选时其子节点 deleted_at 同时过期）。
	if node.Size > 0 {
		if err := c.users.IncrUsedStorage(ctx, c.db, node.UserID, -node.Size); err != nil && !errors.Is(err, domain.ErrQuotaExceeded) {
			logger.L.Warn("trash purge: quota refund failed",
				zap.Int64("user_id", node.UserID), zap.Int64("size", node.Size), zap.Error(err))
		}
	}
	return nil
}
