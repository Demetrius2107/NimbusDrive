package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/Demetrius2107/NimbusDrive/internal/domain"
	"github.com/jmoiron/sqlx"
)

// FileHashRepo 封装 file_hashes 表的数据访问（全局物理存储引用计数）。
type FileHashRepo struct {
	db *sqlx.DB
}

// Get 按哈希查物理存储引用（秒传查询用）。
func (r *FileHashRepo) Get(ctx context.Context, hash string) (*domain.FileHash, error) {
	const q = `SELECT hash_sha256, storage_path, size, ref_count, created_at
		FROM file_hashes WHERE hash_sha256 = $1`
	var h domain.FileHash
	if err := r.db.GetContext(ctx, &h, q, hash); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, domain.ErrNotFound
		}
		return nil, fmt.Errorf("get file_hash: %w", err)
	}
	return &h, nil
}

// Upsert 插入或引用计数 +1。
// 设计文档第九章：INSERT ... ON CONFLICT DO UPDATE SET ref_count=ref_count+1 原子去重。
func (r *FileHashRepo) Upsert(ctx context.Context, hash, storagePath string, size int64) error {
	const q = `
		INSERT INTO file_hashes (hash_sha256, storage_path, size, ref_count)
		VALUES ($1, $2, $3, 1)
		ON CONFLICT (hash_sha256) DO UPDATE
			SET ref_count = file_hashes.ref_count + 1,
			    storage_path = EXCLUDED.storage_path,
			    size = EXCLUDED.size`
	if _, err := r.db.ExecContext(ctx, q, hash, storagePath, size); err != nil {
		return fmt.Errorf("upsert file_hash: %w", err)
	}
	return nil
}

// DecrRef 引用计数 -1；ref_count 降为 0 的行删除（物理对象 GC 由定时任务兜底）。
// 设计文档第九章：先 UPDATE ref_count-1，再 DELETE WHERE ref_count=0。
func (r *FileHashRepo) DecrRef(ctx context.Context, hash string) error {
	const q = `
		UPDATE file_hashes SET ref_count = ref_count - 1
		WHERE hash_sha256 = $1 AND ref_count > 0`
	res, err := r.db.ExecContext(ctx, q, hash)
	if err != nil {
		return fmt.Errorf("decr file_hash ref: %w", err)
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		return domain.ErrNotFound
	}

	// 删除引用计数为 0 的行（不返回错误，因为行可能仍 >0）。
	const delQ = `DELETE FROM file_hashes WHERE hash_sha256 = $1 AND ref_count <= 0`
	if _, err := r.db.ExecContext(ctx, delQ, hash); err != nil {
		return fmt.Errorf("delete zero-ref file_hash: %w", err)
	}
	return nil
}

// ListZeroRef 列出引用计数为 0 的哈希（GC 候选）。
func (r *FileHashRepo) ListZeroRef(ctx context.Context) ([]domain.FileHash, error) {
	const q = `SELECT hash_sha256, storage_path, size, ref_count, created_at
		FROM file_hashes WHERE ref_count = 0`
	var hashes []domain.FileHash
	if err := r.db.SelectContext(ctx, &hashes, q); err != nil {
		return nil, fmt.Errorf("list zero-ref file_hash: %w", err)
	}
	return hashes, nil
}
