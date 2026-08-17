package adminstore

import (
	"context"
	"fmt"

	"gorm.io/gorm"
)

// UserRepo 管理后台用户查询/操作（GORM）。
type UserRepo struct {
	db *gorm.DB
}

func NewUserRepo(db *gorm.DB) *UserRepo {
	return &UserRepo{db: db}
}

// ListUsers 分页查询用户。search 匹配用户名/邮箱（ILIKE），status=0 表示全部。
func (r *UserRepo) ListUsers(ctx context.Context, page, size int, search string, status int16) ([]User, int64, error) {
	if page < 1 {
		page = 1
	}
	if size < 1 || size > 200 {
		size = 50
	}
	offset := (page - 1) * size

	q := r.db.WithContext(ctx).Model(&User{})
	if search != "" {
		pattern := "%" + search + "%"
		q = q.Where("username ILIKE ? OR email ILIKE ?", pattern, pattern)
	}
	if status > 0 {
		q = q.Where("status = ?", status)
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("count users: %w", err)
	}

	var users []User
	if err := q.Order("created_at DESC").Limit(size).Offset(offset).Find(&users).Error; err != nil {
		return nil, 0, fmt.Errorf("list users: %w", err)
	}
	return users, total, nil
}

// GetByID 查单个用户。
func (r *UserRepo) GetByID(ctx context.Context, id int64) (*User, error) {
	var u User
	if err := r.db.WithContext(ctx).First(&u, id).Error; err != nil {
		return nil, fmt.Errorf("get user: %w", err)
	}
	return &u, nil
}

// UpdateStatus 修改用户状态（1=正常 2=封禁）。返回旧状态供日志记录。
func (r *UserRepo) UpdateStatus(ctx context.Context, id int64, status int16) (oldStatus int16, err error) {
	var u User
	if err := r.db.WithContext(ctx).First(&u, id).Error; err != nil {
		return 0, fmt.Errorf("get user for update: %w", err)
	}
	oldStatus = u.Status
	if err := r.db.WithContext(ctx).Model(&User{}).Where("id = ?", id).Update("status", status).Error; err != nil {
		return 0, fmt.Errorf("update user status: %w", err)
	}
	return oldStatus, nil
}

// UpdateQuota 修改用户配额。返回旧配额供日志记录。
func (r *UserRepo) UpdateQuota(ctx context.Context, id int64, quota int64) (oldQuota int64, err error) {
	var u User
	if err := r.db.WithContext(ctx).First(&u, id).Error; err != nil {
		return 0, fmt.Errorf("get user for update: %w", err)
	}
	oldQuota = u.StorageQuota
	if err := r.db.WithContext(ctx).Model(&User{}).Where("id = ?", id).Update("storage_quota", quota).Error; err != nil {
		return 0, fmt.Errorf("update user quota: %w", err)
	}
	return oldQuota, nil
}
