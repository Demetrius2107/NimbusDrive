package cron

import (
	"context"
	"errors"
	"time"

	"github.com/Demetrius2107/NimbusDrive/internal/domain"
	"github.com/Demetrius2107/NimbusDrive/internal/logger"
	"github.com/Demetrius2107/NimbusDrive/internal/metrics"
	"github.com/Demetrius2107/NimbusDrive/internal/storage"
	"github.com/Demetrius2107/NimbusDrive/internal/store"
	"go.uber.org/zap"
)

// sessionExpirer 抽象 UploadSessionRepo 的过期清理能力。
type sessionExpirer interface {
	MarkExpired(ctx context.Context) (int, error)
	ListExpired(ctx context.Context, limit int) ([]domain.UploadSession, error)
	DeleteByID(ctx context.Context, id string) error
}

// fileHardDeleter 抽象 FileRepo.HardDelete（清理 init 占位文件）。
type fileHardDeleter interface {
	HardDelete(ctx context.Context, id int64) error
}

// multipartAborter 抽象 MinIO.AbortMultipartUpload。
type multipartAborter interface {
	AbortMultipartUpload(ctx context.Context, objectKey, uploadID string) error
}

// SessionExpiryCron 上传会话过期清理定时任务。
//
// 清理流程：
//  1. MarkExpired 批量将 active 且 expires_at < now 的会话标 expired（已有方法，原为 dead code）
//  2. ListExpired 取刚标记的会话（批次化）
//  3. 对每个会话：HardDelete(init 占位文件) + AbortMultipartUpload(若 upload_id 非空)
//  4. DeleteByID 删会话行（清理完毕即删，避免重复扫描）
//
// 失败不阻塞：单个会话清理失败记日志+继续下一个，下轮重试（行未删，仍在 expired 集合）。
type SessionExpiryCron struct {
	sessions  sessionExpirer
	files     fileHardDeleter
	mc        multipartAborter
	batchSize int
	interval  time.Duration
}

// NewSessionExpiryCron 构造。任一依赖为 nil 时 Start 不启动（降级）。
func NewSessionExpiryCron(sessions *store.UploadSessionRepo, files *store.FileRepo, mc *storage.MinIO, batchSize, intervalSec int) *SessionExpiryCron {
	return &SessionExpiryCron{
		sessions:  sessions,
		files:     files,
		mc:        mc,
		batchSize: batchSize,
		interval:  time.Duration(intervalSec) * time.Second,
	}
}

// Start 启动后台 goroutine，返回停止函数。依赖为 nil 直接返回空停止函数。
func (c *SessionExpiryCron) Start(ctx context.Context) func() {
	if c.sessions == nil || c.files == nil || c.mc == nil {
		logger.L.Warn("session expiry cron disabled: dependency nil")
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

// tick 执行一次清理批次。
func (c *SessionExpiryCron) tick(ctx context.Context) {
	// 1. 批量标记过期。
	marked, err := c.sessions.MarkExpired(ctx)
	if err != nil {
		logger.L.Error("session expiry: mark expired failed", zap.Error(err))
		metrics.Default().SessionExpirySwept.WithLabelValues("mark_error").Inc()
		return
	}
	if marked == 0 {
		return // 无新过期会话
	}

	// 2. 取刚标记的会话。
	sessions, err := c.sessions.ListExpired(ctx, c.batchSize)
	if err != nil {
		logger.L.Error("session expiry: list expired failed", zap.Error(err))
		return
	}

	var cleaned int
	for _, s := range sessions {
		if err := c.cleanSession(ctx, s); err != nil {
			logger.L.Error("session expiry: clean failed, skipping",
				zap.String("session_id", s.ID), zap.Error(err))
			metrics.Default().SessionExpirySwept.WithLabelValues("error").Inc()
			continue
		}
		cleaned++
		metrics.Default().SessionExpirySwept.WithLabelValues("success").Inc()
	}
	logger.L.Info("session expiry tick done",
		zap.Int("marked", marked), zap.Int("cleaned", cleaned))
}

// cleanSession 清理单个过期会话：占位文件 + Multipart + 会话行。
func (c *SessionExpiryCron) cleanSession(ctx context.Context, s domain.UploadSession) error {
	// 3a. 删 init 占位文件（会话过期=上传未完成，files 行仍是 init 状态）。
	if err := c.files.HardDelete(ctx, s.FileID); err != nil && !errors.Is(err, domain.ErrNotFound) {
		return err
	}
	// 3b. 中止 MinIO Multipart Upload（若 upload_id 非空，即已开始上传分块）。
	if s.UploadID != "" {
		objectKey := storage.ObjectKey(s.HashSHA256)
		if err := c.mc.AbortMultipartUpload(ctx, objectKey, s.UploadID); err != nil {
			logger.L.Warn("session expiry: abort multipart failed",
				zap.String("session_id", s.ID), zap.String("upload_id", s.UploadID), zap.Error(err))
			// 不阻断：Multipart 残留由 MinIO 侧 lifecycle 兜底，继续删会话行
		}
	}
	// 4. 删会话行。
	if err := c.sessions.DeleteByID(ctx, s.ID); err != nil && !errors.Is(err, domain.ErrNotFound) {
		return err
	}
	return nil
}
