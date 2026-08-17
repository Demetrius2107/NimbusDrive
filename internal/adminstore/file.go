package adminstore

import "time"

// File 对应 files 表（管理后台只读 GORM 视图）。
// 不用于写入，仅提供管理端全局文件列表查询。
type File struct {
	ID          int64     `gorm:"primaryKey" json:"id"`
	UserID      int64     `json:"user_id"`
	ParentID    *int64    `json:"parent_id,omitempty"`
	Name        string    `gorm:"size:255" json:"name"`
	Size        int64     `json:"size"`
	MimeType    string    `gorm:"size:255" json:"mime_type"`
	IsFolder    bool      `json:"is_folder"`
	HashSHA256  *string   `gorm:"column:hash_sha256" json:"hash_sha256,omitempty"`
	ChunkCount  int       `json:"chunk_count"`
	Status      string    `gorm:"size:32" json:"status"`
	StoragePath *string   `json:"storage_path,omitempty"`
	DeletedAt   *time.Time `json:"deleted_at,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// TableName 显式指定表名。
func (File) TableName() string { return "files" }
