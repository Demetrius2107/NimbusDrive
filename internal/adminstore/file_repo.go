package adminstore

import (
	"context"
	"fmt"

	"gorm.io/gorm"
)

// FileRepo 管理后台文件查询（只读 GORM 视图）。
type FileRepo struct {
	db *gorm.DB
}

func NewFileRepo(db *gorm.DB) *FileRepo {
	return &FileRepo{db: db}
}

// ListFiles 分页查询全局文件。userID > 0 时筛选某用户。包含已删除（回收站）的文件。
func (r *FileRepo) ListFiles(ctx context.Context, page, size int, userID int64) ([]File, int64, error) {
	if page < 1 {
		page = 1
	}
	if size < 1 || size > 200 {
		size = 50
	}
	offset := (page - 1) * size

	q := r.db.WithContext(ctx).Model(&File{})
	if userID > 0 {
		q = q.Where("user_id = ?", userID)
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("count files: %w", err)
	}

	var files []File
	if err := q.Order("created_at DESC").Limit(size).Offset(offset).Find(&files).Error; err != nil {
		return nil, 0, fmt.Errorf("list files: %w", err)
	}
	if files == nil {
		// 空结果序列化为 [] 而非 null，前端契约假定列表永远是数组
		files = []File{}
	}
	return files, total, nil
}
