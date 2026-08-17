// Package adminstore 用户管理 GORM 模型。
// 与 domain.User (sqlx) 分离，避免 db/gorm tag 混用。
package adminstore

import "time"

// User 对应 users 表（管理后台 GORM 视图）。
type User struct {
	ID            int64     `gorm:"primaryKey" json:"id"`
	Username      string    `gorm:"size:64;uniqueIndex" json:"username"`
	Email         string    `gorm:"size:255;uniqueIndex" json:"email"`
	PasswordHash  string    `gorm:"size:255" json:"-"`
	StorageQuota  int64     `gorm:"default:0" json:"storage_quota"`
	UsedStorage   int64     `gorm:"default:0" json:"used_storage"`
	Status        int16     `gorm:"default:1" json:"status"` // 1=正常 2=封禁
	IsAdmin       bool      `gorm:"default:false" json:"is_admin"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// TableName 显式指定表名（GORM 默认会复数化 user → users，这里显式避免歧义）。
func (User) TableName() string { return "users" }
