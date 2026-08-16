// Package store 封装 sqlx + pgx 数据访问层，是核心链路（用户端）的数据权威。
// 管理后台 CRUD 走 internal/adminstore (GORM)，二者不混用。
package store

import (
	"context"
	"fmt"

	"github.com/jmoiron/sqlx"
	_ "github.com/jackc/pgx/v5/stdlib" // 注册 "pgx" 驱动给 database/sql
)

// Store 持有 *sqlx.DB，供各 repository 共享连接池。
type Store struct {
	DB *sqlx.DB
}

// New 创建 store。用 pgx 的 stdlib 驱动桥接给 sqlx，连接池由 database/sql 管理。
func New(ctx context.Context, dsn string, maxOpen, maxIdle int) (*Store, error) {
	db, err := sqlx.ConnectContext(ctx, "pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("connect postgres: %w", err)
	}
	if maxOpen > 0 {
		db.DB.SetMaxOpenConns(maxOpen)
	}
	if maxIdle > 0 {
		db.DB.SetMaxIdleConns(maxIdle)
	}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}
	return &Store{DB: db}, nil
}

// Close 释放连接池。
func (s *Store) Close() error {
	if s.DB != nil {
		return s.DB.Close()
	}
	return nil
}
