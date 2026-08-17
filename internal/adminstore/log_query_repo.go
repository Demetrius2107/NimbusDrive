package adminstore

import (
	"context"
	"fmt"

	"gorm.io/gorm"
)

// LogQueryRepo 操作日志查询（只读）。写入由 LogAggregator 负责。
type LogQueryRepo struct {
	db *gorm.DB
}

func NewLogQueryRepo(db *gorm.DB) *LogQueryRepo {
	return &LogQueryRepo{db: db}
}

// List 分页查询操作日志。action 非空时筛选，actorID > 0 时筛选。按 created_at DESC。
func (r *LogQueryRepo) List(ctx context.Context, page, size int, action string, actorID int64) ([]OperationLog, int64, error) {
	if page < 1 {
		page = 1
	}
	if size < 1 || size > 200 {
		size = 50
	}
	offset := (page - 1) * size

	q := r.db.WithContext(ctx).Model(&OperationLog{})
	if action != "" {
		q = q.Where("action = ?", action)
	}
	if actorID > 0 {
		q = q.Where("actor_id = ?", actorID)
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("count operation_logs: %w", err)
	}

	var logs []OperationLog
	if err := q.Order("created_at DESC").Limit(size).Offset(offset).Find(&logs).Error; err != nil {
		return nil, 0, fmt.Errorf("list operation_logs: %w", err)
	}
	return logs, total, nil
}
