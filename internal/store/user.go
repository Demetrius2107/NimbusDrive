package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/Demetrius2107/NimbusDrive/internal/domain"
	"github.com/jmoiron/sqlx"
)

// UserRepo 封装 users 表的数据访问。
type UserRepo struct {
	db *sqlx.DB
}

// Create 创建用户。返回带 ID 的 User。
// username / email 唯一约束冲突时返回 domain.ErrConflict。
func (r *UserRepo) Create(ctx context.Context, username, email, passwordHash string) (*domain.User, error) {
	const q = `
		INSERT INTO users (username, email, password_hash)
		VALUES ($1, $2, $3)
		RETURNING id, username, email, password_hash, storage_quota, used_storage, status, is_admin, created_at, updated_at`
	var u domain.User
	if err := r.db.GetContext(ctx, &u, q, username, email, passwordHash); err != nil {
		if isUniqueViolation(err) {
			return nil, domain.ErrConflict
		}
		return nil, fmt.Errorf("insert user: %w", err)
	}
	return &u, nil
}

// GetByID 按 ID 查用户。
func (r *UserRepo) GetByID(ctx context.Context, id int64) (*domain.User, error) {
	const q = `SELECT id, username, email, password_hash, storage_quota, used_storage, status, is_admin, created_at, updated_at
		FROM users WHERE id = $1`
	var u domain.User
	if err := r.db.GetContext(ctx, &u, q, id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, domain.ErrNotFound
		}
		return nil, fmt.Errorf("get user by id: %w", err)
	}
	return &u, nil
}

// GetByUsername 按用户名查用户（登录用）。
func (r *UserRepo) GetByUsername(ctx context.Context, username string) (*domain.User, error) {
	const q = `SELECT id, username, email, password_hash, storage_quota, used_storage, status, is_admin, created_at, updated_at
		FROM users WHERE username = $1`
	var u domain.User
	if err := r.db.GetContext(ctx, &u, q, username); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, domain.ErrNotFound
		}
		return nil, fmt.Errorf("get user by username: %w", err)
	}
	return &u, nil
}

// IncrUsedStorage 原子扣减/回补配额。
// delta > 0 表示增加已用（上传完成扣减），delta < 0 表示减少（删除回补）。
// 条件：used_storage + delta <= storage_quota；不满足返回 domain.ErrQuotaExceeded。
// 设计文档第九章并发策略：原子条件更新，affected=0 即超限。
// ext 接受 *sqlx.DB 或 *sqlx.Tx，使调用方可在事务内复用此方法（executor 接口模式）。
func (r *UserRepo) IncrUsedStorage(ctx context.Context, ext sqlx.ExtContext, userID, delta int64) error {
	const q = `
		UPDATE users
		SET used_storage = used_storage + $2
		WHERE id = $1 AND used_storage + $2 <= storage_quota AND used_storage + $2 >= 0`
	res, err := ext.ExecContext(ctx, q, userID, delta)
	if err != nil {
		return fmt.Errorf("incr used_storage: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("incr used_storage rows affected: %w", err)
	}
	if affected == 0 {
		return domain.ErrQuotaExceeded
	}
	return nil
}

// SetQuota 修改用户总配额（管理端用）。
func (r *UserRepo) SetQuota(ctx context.Context, userID, quota int64) error {
	const q = `UPDATE users SET storage_quota = $2 WHERE id = $1`
	res, err := r.db.ExecContext(ctx, q, userID, quota)
	if err != nil {
		return fmt.Errorf("set quota: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("set quota rows affected: %w", err)
	}
	if affected == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// isUniqueViolation 判断是否为唯一约束冲突（pgx stdlib 返回的 error 字符串匹配）。
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	// pgx stdlib 通过 *pq.Error 暴露码；为避免引入 pq 依赖，用字符串匹配兜底。
	msg := err.Error()
	return containsAny(msg, "unique constraint", "duplicate key")
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if len(sub) > 0 && indexOf(s, sub) >= 0 {
			return true
		}
	}
	return false
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
