package adminstore

import (
	"context"
	"fmt"
)

// Migrate 自动建表/补列。对已存在的表是幂等的（additive）。
// 注册所有 admin GORM 模型。
func (d *DB) Migrate(ctx context.Context) error {
	if err := d.GORM.WithContext(ctx).AutoMigrate(
		&User{},
		&OperationLog{},
		&File{},
		&QuotaPeriod{},
	); err != nil {
		return fmt.Errorf("admin auto-migrate: %w", err)
	}
	return nil
}
