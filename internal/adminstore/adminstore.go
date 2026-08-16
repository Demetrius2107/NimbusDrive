// Package adminstore 封装 GORM 数据访问层，仅用于管理后台 CRUD。
// 与 internal/store (sqlx) 职责边界严格分离：管理后台操作走 GORM，
// 用户核心操作走 sqlx，互不混用。详见《设计文档》第三章。
//
// 骨架阶段仅提供 DB 初始化；具体 model（用户管理、文件管理、哈希池管理、日志管理）
// 在实现管理端接口时补充。
package adminstore

import (
	"context"
	"fmt"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// DB 持有 gorm.DB。
type DB struct {
	GORM *gorm.DB
}

// New 创建 gorm 连接。
func New(ctx context.Context, dsn string, logLevel string) (*DB, error) {
	var lvl logger.LogLevel
	switch logLevel {
	case "silent":
		lvl = logger.Silent
	case "error":
		lvl = logger.Error
	case "warn":
		lvl = logger.Warn
	case "info":
		lvl = logger.Info
	default:
		lvl = logger.Warn
	}

	gdb, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(lvl),
	})
	if err != nil {
		return nil, fmt.Errorf("open gorm: %w", err)
	}

	sqlDB, err := gdb.DB()
	if err != nil {
		return nil, fmt.Errorf("get underlying sql db: %w", err)
	}
	if err := sqlDB.PingContext(ctx); err != nil {
		return nil, fmt.Errorf("ping gorm postgres: %w", err)
	}
	return &DB{GORM: gdb}, nil
}

// Close 释放连接。
func (d *DB) Close() error {
	if d.GORM != nil {
		sqlDB, err := d.GORM.DB()
		if err != nil {
			return err
		}
		return sqlDB.Close()
	}
	return nil
}
