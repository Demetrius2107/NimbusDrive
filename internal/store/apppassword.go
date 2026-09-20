package store

import (
	"context"
	"fmt"
	"time"

	"github.com/Demetrius2107/NimbusDrive/internal/domain"
	"github.com/jmoiron/sqlx"
)

// AppPasswordRepo 封装 app_passwords 表的数据访问（应用专用密码）。
// 供 WebDAV Basic Auth 校验与 APIServer 管理端点使用。
type AppPasswordRepo struct {
	db *sqlx.DB
}

// Create 插入一条应用密码（passwordHash 为 bcrypt 结果），返回行 ID。
func (r *AppPasswordRepo) Create(ctx context.Context, userID int64, name, passwordHash string) (int64, error) {
	var id int64
	const q = `INSERT INTO app_passwords (user_id, name, password_hash)
		VALUES ($1, $2, $3) RETURNING id`
	if err := r.db.GetContext(ctx, &id, q, userID, name, passwordHash); err != nil {
		return 0, fmt.Errorf("insert app_password: %w", err)
	}
	return id, nil
}

// ListByUser 列出用户全部应用密码（含已吊销，保留审计痕迹）。
func (r *AppPasswordRepo) ListByUser(ctx context.Context, userID int64) ([]domain.AppPassword, error) {
	const q = `SELECT id, user_id, name, password_hash, last_used_at, revoked, created_at
		FROM app_passwords WHERE user_id = $1 ORDER BY id`
	var list []domain.AppPassword
	if err := r.db.SelectContext(ctx, &list, q, userID); err != nil {
		return nil, fmt.Errorf("list app_passwords: %w", err)
	}
	return nonNil(list), nil
}

// ListActiveByUser 列出用户未吊销的应用密码（Basic Auth 逐个 bcrypt 比对用）。
func (r *AppPasswordRepo) ListActiveByUser(ctx context.Context, userID int64) ([]domain.AppPassword, error) {
	const q = `SELECT id, user_id, name, password_hash, last_used_at, revoked, created_at
		FROM app_passwords WHERE user_id = $1 AND revoked = FALSE ORDER BY id`
	var list []domain.AppPassword
	if err := r.db.SelectContext(ctx, &list, q, userID); err != nil {
		return nil, fmt.Errorf("list active app_passwords: %w", err)
	}
	return list, nil
}

// Revoke 吊销指定应用密码（仅本人）。
func (r *AppPasswordRepo) Revoke(ctx context.Context, userID, id int64) error {
	const q = `UPDATE app_passwords SET revoked = TRUE WHERE id = $1 AND user_id = $2 AND revoked = FALSE`
	res, err := r.db.ExecContext(ctx, q, id, userID)
	if err != nil {
		return fmt.Errorf("revoke app_password: %w", err)
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// TouchLastUsed 懒更新最近使用时间。
// 每次认证都 UPDATE 代价高（行锁 + WAL），与上次值差超过 throttle 才写；
// last_used_at 为空（从未用过）直接写。
func (r *AppPasswordRepo) TouchLastUsed(ctx context.Context, id int64, lastUsed *time.Time, now time.Time) {
	const throttle = time.Hour
	if lastUsed != nil && now.Sub(*lastUsed) < throttle {
		return
	}
	const q = `UPDATE app_passwords SET last_used_at = $1 WHERE id = $2`
	if _, err := r.db.ExecContext(ctx, q, now, id); err != nil {
		// 非关键路径，失败仅影响统计展示
		return
	}
}
