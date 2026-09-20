package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/Demetrius2107/NimbusDrive/internal/domain"
	"github.com/jmoiron/sqlx"
)

// QuotaRepo 封装 quota_periods 表的数据访问（用户核心链路，sqlx）。
// 记录用户月度传输配额用量，每月一行，period 格式 "YYYY-MM"。
type QuotaRepo struct {
	db *sqlx.DB
}

// currentPeriod 返回当前 UTC 月份的 "YYYY-MM" 格式。
func currentPeriod() string {
	return time.Now().UTC().Format("2006-01")
}

// quotaCols 是 quota_periods 表的查询列，与 domain.QuotaPeriod 的 db tag 对齐。
const quotaCols = `id, user_id, period, upload_bytes, download_bytes, upload_quota, download_quota, reset_at, created_at, updated_at`

// GetOrCreateCurrent 获取用户当月配额行，不存在则创建。
// 创建时 upload_quota/download_quota 从 users.storage_quota 派生（MVP 简化：传输配额=存储配额）。
func (r *QuotaRepo) GetOrCreateCurrent(ctx context.Context, userID int64) (*domain.QuotaPeriod, error) {
	period := currentPeriod()

	// 先查
	var qp domain.QuotaPeriod
	err := r.db.GetContext(ctx, &qp,
		`SELECT `+quotaCols+` FROM quota_periods WHERE user_id = $1 AND period = $2`,
		userID, period)
	if err == nil {
		return &qp, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("get quota period: %w", err)
	}

	// 不存在：从 users 表读 storage_quota 派生传输配额，插入当月行
	var storageQuota int64
	if err := r.db.GetContext(ctx, &storageQuota,
		`SELECT storage_quota FROM users WHERE id = $1`, userID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, domain.ErrNotFound
		}
		return nil, fmt.Errorf("get user storage_quota: %w", err)
	}

	now := time.Now().UTC()
	if _, err := r.db.ExecContext(ctx,
		`INSERT INTO quota_periods (user_id, period, upload_quota, download_quota, reset_at)
		 VALUES ($1, $2, $3, $3, $4)
		 ON CONFLICT (user_id, period) DO NOTHING`,
		userID, period, storageQuota, now); err != nil {
		return nil, fmt.Errorf("insert quota period: %w", err)
	}

	// 重新查询（ON CONFLICT 时行已存在，需读到已有值）
	if err := r.db.GetContext(ctx, &qp,
		`SELECT `+quotaCols+` FROM quota_periods WHERE user_id = $1 AND period = $2`,
		userID, period); err != nil {
		return nil, fmt.Errorf("get quota period after create: %w", err)
	}
	return &qp, nil
}

// CheckUpload 检查当月上传配额是否足够（只检查不扣）。
// upload_quota = 0 表示不限。size 为本次上传字节数。
func (r *QuotaRepo) CheckUpload(ctx context.Context, userID, size int64) error {
	qp, err := r.GetOrCreateCurrent(ctx, userID)
	if err != nil {
		return err
	}
	if qp.UploadQuota == 0 {
		return nil // 不限
	}
	if qp.UploadBytes+size > qp.UploadQuota {
		return domain.ErrQuotaExceeded
	}
	return nil
}

// CheckStorage 预检用户存储配额是否足够（users 表，只检查不扣）。
// 与 handler.UploadHandler.precheckQuota 的存储部分同语义；WebDAV PUT
// 挂载层用，超限在挂载层直接拦 507（FileSystem 层错误只映射 404/405）。
func (r *QuotaRepo) CheckStorage(ctx context.Context, userID, size int64) error {
	var quota, used int64
	err := r.db.QueryRowxContext(ctx,
		`SELECT storage_quota, used_storage FROM users WHERE id = $1`, userID).Scan(&quota, &used)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.ErrNotFound
		}
		return fmt.Errorf("query storage quota: %w", err)
	}
	if used+size > quota {
		return domain.ErrQuotaExceeded
	}
	return nil
}

// IncrUpload 原子增加当月上传字节，带配额上限校验。
// upload_quota = 0 表示不限。affected=0 → 超限 → ErrQuotaExceeded。
func (r *QuotaRepo) IncrUpload(ctx context.Context, userID, delta int64) error {
	period := currentPeriod()
	res, err := r.db.ExecContext(ctx, `
		UPDATE quota_periods
		SET upload_bytes = upload_bytes + $2
		WHERE user_id = $1 AND period = $3
		  AND (upload_quota = 0 OR upload_bytes + $2 <= upload_quota)`,
		userID, delta, period)
	if err != nil {
		return fmt.Errorf("incr upload bytes: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("incr upload bytes rows affected: %w", err)
	}
	if affected == 0 {
		return domain.ErrQuotaExceeded
	}
	return nil
}

// IncrDownload 原子增加当月下载字节，带配额上限校验。
// download_quota = 0 表示不限。affected=0 → 超限 → ErrQuotaExceeded。
func (r *QuotaRepo) IncrDownload(ctx context.Context, userID, delta int64) error {
	period := currentPeriod()
	res, err := r.db.ExecContext(ctx, `
		UPDATE quota_periods
		SET download_bytes = download_bytes + $2
		WHERE user_id = $1 AND period = $3
		  AND (download_quota = 0 OR download_bytes + $2 <= download_quota)`,
		userID, delta, period)
	if err != nil {
		return fmt.Errorf("incr download bytes: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("incr download bytes rows affected: %w", err)
	}
	if affected == 0 {
		return domain.ErrQuotaExceeded
	}
	return nil
}

// ResetMonthly 为用户创建新月度 period 行（cron 调用）。
// 已存在则不覆盖（ON CONFLICT DO NOTHING）。upQuota/dlQuota = 0 表示不限。
func (r *QuotaRepo) ResetMonthly(ctx context.Context, userID int64, period string, upQuota, dlQuota int64) error {
	now := time.Now().UTC()
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO quota_periods (user_id, period, upload_quota, download_quota, reset_at)
		 VALUES ($1, $2, $3, $4, $5)
		 ON CONFLICT (user_id, period) DO NOTHING`,
		userID, period, upQuota, dlQuota, now)
	if err != nil {
		return fmt.Errorf("reset monthly quota: %w", err)
	}
	return nil
}

// ResetMonthlyAll 为所有活跃用户创建当月 period 行（批量月度重置）。
// 从 users 表派生传输配额（= storage_quota）。已存在则跳过。
func (r *QuotaRepo) ResetMonthlyAll(ctx context.Context, period string) (int64, error) {
	now := time.Now().UTC()
	res, err := r.db.ExecContext(ctx,
		`INSERT INTO quota_periods (user_id, period, upload_quota, download_quota, reset_at)
		 SELECT id, $1, storage_quota, storage_quota, $2
		 FROM users WHERE status = 1
		 ON CONFLICT (user_id, period) DO NOTHING`,
		period, now)
	if err != nil {
		return 0, fmt.Errorf("reset monthly quota all: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("reset monthly quota all rows affected: %w", err)
	}
	return affected, nil
}
