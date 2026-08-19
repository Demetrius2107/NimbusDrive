package adminstore

import "time"

// QuotaPeriod 对应 quota_periods 表，记录用户月度传输配额用量。每月一行。
// period 格式 "YYYY-MM"。upload_quota/download_quota = 0 表示不限。
type QuotaPeriod struct {
	ID            int64     `gorm:"primaryKey" json:"id"`
	UserID        int64     `gorm:"uniqueIndex:idx_quota_period_user_period,priority:1" json:"user_id"`
	Period        string    `gorm:"size:7;uniqueIndex:idx_quota_period_user_period,priority:2" json:"period"` // "YYYY-MM"
	UploadBytes   int64     `gorm:"default:0" json:"upload_bytes"`
	DownloadBytes int64     `gorm:"default:0" json:"download_bytes"`
	UploadQuota   int64     `gorm:"default:0" json:"upload_quota"`     // 0 = 不限
	DownloadQuota int64     `gorm:"default:0" json:"download_quota"`  // 0 = 不限
	ResetAt       time.Time `json:"reset_at"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// TableName 显式指定表名。
func (QuotaPeriod) TableName() string { return "quota_periods" }
