package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/Demetrius2107/NimbusDrive/internal/domain"
	"github.com/jmoiron/sqlx"
)

// ShareRepo 封装 shares 表的数据访问（分享链接）。
// 设计文档 4.7：PG 为事实源 + Redis 缓存。
type ShareRepo struct {
	db *sqlx.DB
}

// Create 创建分享。返回分享 ID。
func (r *ShareRepo) Create(ctx context.Context, s *domain.Share) (string, error) {
	const q = `
		INSERT INTO shares (id, user_id, file_id, password_hash, expires_at, max_access, status)
		VALUES ($1, $2, $3, $4, $5, $6, 'ready')`
	if _, err := r.db.ExecContext(ctx, q,
		s.ID, s.UserID, s.FileID, s.PasswordHash, s.ExpiresAt, s.MaxAccess,
	); err != nil {
		return "", fmt.Errorf("insert share: %w", err)
	}
	return s.ID, nil
}

// Get 按 ID 查分享。
func (r *ShareRepo) Get(ctx context.Context, id string) (*domain.Share, error) {
	const q = `SELECT id, user_id, file_id, password_hash, expires_at, max_access, access_count, status, created_at, updated_at
		FROM shares WHERE id = $1`
	var s domain.Share
	if err := r.db.GetContext(ctx, &s, q, id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, domain.ErrNotFound
		}
		return nil, fmt.Errorf("get share: %w", err)
	}
	return &s, nil
}

// IncrAccess 访问计数 +1，带次数上限校验。
// 设计文档第九章：WHERE max_access IS NULL OR access_count < max_access；affected=0 即超限。
func (r *ShareRepo) IncrAccess(ctx context.Context, id string) error {
	const q = `
		UPDATE shares SET access_count = access_count + 1, status = 'active'
		WHERE id = $1 AND status IN ('ready','active')
		  AND (max_access IS NULL OR access_count < max_access)`
	res, err := r.db.ExecContext(ctx, q, id)
	if err != nil {
		return fmt.Errorf("incr share access: %w", err)
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		return domain.ErrForbidden
	}
	return nil
}

// Cancel 吊销分享。
func (r *ShareRepo) Cancel(ctx context.Context, id string) error {
	const q = `UPDATE shares SET status = 'cancelled' WHERE id = $1 AND status IN ('ready','active')`
	res, err := r.db.ExecContext(ctx, q, id)
	if err != nil {
		return fmt.Errorf("cancel share: %w", err)
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// ListByUser 列出某用户的分享（分页）。
func (r *ShareRepo) ListByUser(ctx context.Context, userID int64, page, size int) ([]domain.Share, int, error) {
	if page < 1 {
		page = 1
	}
	if size < 1 || size > 200 {
		size = 50
	}
	offset := (page - 1) * size

	const q = `SELECT id, user_id, file_id, password_hash, expires_at, max_access, access_count, status, created_at, updated_at
		FROM shares WHERE user_id = $1
		ORDER BY created_at DESC
		LIMIT $2 OFFSET $3`
	var shares []domain.Share
	if err := r.db.SelectContext(ctx, &shares, q, userID, size, offset); err != nil {
		return nil, 0, fmt.Errorf("list shares: %w", err)
	}

	const countQ = `SELECT count(*) FROM shares WHERE user_id = $1`
	var total int
	if err := r.db.GetContext(ctx, &total, countQ, userID); err != nil {
		return nil, 0, fmt.Errorf("count shares: %w", err)
	}
	return shares, total, nil
}

// MarkExpired 标记分享已过期（缓存层发现 expires_at < now 时回写）。
func (r *ShareRepo) MarkExpired(ctx context.Context, id string) error {
	const q = `UPDATE shares SET status = 'expired' WHERE id = $1 AND status IN ('ready','active')`
	_, err := r.db.ExecContext(ctx, q, id)
	if err != nil {
		return fmt.Errorf("mark share expired: %w", err)
	}
	return nil
}

// GetByFile 按文件 ID 查分享（创建时校验是否已存在活跃分享用）。
func (r *ShareRepo) GetByFile(ctx context.Context, fileID int64) (*domain.Share, error) {
	const q = `SELECT id, user_id, file_id, password_hash, expires_at, max_access, access_count, status, created_at, updated_at
		FROM shares WHERE file_id = $1 AND status IN ('ready','active')
		ORDER BY created_at DESC LIMIT 1`
	var s domain.Share
	if err := r.db.GetContext(ctx, &s, q, fileID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, domain.ErrNotFound
		}
		return nil, fmt.Errorf("get share by file: %w", err)
	}
	return &s, nil
}
