package adminstore

import (
	"encoding/json"
	"time"
)

// OperationLog 对应 operation_logs 表（追加-only，无 updated_at）。
// 管理操作产生的审计日志，通过 LogAggregator 异步批量写入。
type OperationLog struct {
	ID         int64           `gorm:"primaryKey" json:"id"`
	ActorID    *int64          `json:"actor_id,omitempty"`
	ActorType  string          `gorm:"size:16" json:"actor_type"` // "admin" / "user" / "system"
	Action     string          `gorm:"size:64" json:"action"`     // 如 "admin.user.status"
	TargetType *string         `gorm:"size:32" json:"target_type,omitempty"`
	TargetID   *string         `gorm:"size:64" json:"target_id,omitempty"`
	IP         *string         `gorm:"type:inet" json:"ip,omitempty"`
	Detail     json.RawMessage `gorm:"type:jsonb" json:"detail,omitempty"`
	CreatedAt  time.Time       `gorm:"autoCreateTime" json:"created_at"`
}

// TableName 显式指定表名。
func (OperationLog) TableName() string { return "operation_logs" }
