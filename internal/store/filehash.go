package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

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
// re-reference 时清 zero_ref_at=NULL：把行移出 GC 候选集，避免 grace 窗口内并发
// 重传同哈希时 GC 误删（tombstone + grace 的竞态防护）。
// ext 接受 *sqlx.DB 或 *sqlx.Tx，使调用方可在事务内复用此方法（executor 接口模式）。
func (r *FileHashRepo) Upsert(ctx context.Context, ext sqlx.ExtContext, hash, storagePath string, size int64) error {
	const q = `
		INSERT INTO file_hashes (hash_sha256, storage_path, size, ref_count)
		VALUES ($1, $2, $3, 1)
		ON CONFLICT (hash_sha256) DO UPDATE
			SET ref_count = file_hashes.ref_count + 1,
			    storage_path = EXCLUDED.storage_path,
			    size = EXCLUDED.size,
			    zero_ref_at = NULL`
	if _, err := ext.ExecContext(ctx, q, hash, storagePath, size); err != nil {
		return fmt.Errorf("upsert file_hash: %w", err)
	}
	return nil
}

// DecrRef 引用计数 -1；ref_count 降为 0 时记 zero_ref_at 墓碑，**不删行**。
//
// 设计变更（迁移 0004）：原实现立即 DELETE 零引用行，导致 ListZeroRef
// （WHERE ref_count=0）永远查不到——物理对象一旦 orphan 即无迹可循，GC
// 在结构上不可能。改为保留行 + 记墓碑时间，GC cron 按 grace 窗口回收
// （RemoveObject 后调 DeleteIfZeroRef 删行）。
//
// 竞态分析：grace 窗口内并发 re-reference 会被 Upsert 的 zero_ref_at=NULL
// 移出 GC 候选，安全；窗口外的残存竞态以长 grace + 监控指标兜底。
//
// ext 接受 *sqlx.DB 或 *sqlx.Tx。
func (r *FileHashRepo) DecrRef(ctx context.Context, ext sqlx.ExtContext, hash string) error {
	const q = `
		UPDATE file_hashes
		SET ref_count = ref_count - 1,
		    zero_ref_at = CASE WHEN ref_count - 1 = 0 THEN now() ELSE zero_ref_at END
		WHERE hash_sha256 = $1 AND ref_count > 0`
	res, err := ext.ExecContext(ctx, q, hash)
	if err != nil {
		return fmt.Errorf("decr file_hash ref: %w", err)
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// ListZeroRef 列出零引用且墓碑早于 before 的哈希（GC 候选）。
// before = now()-grace，grace 窗口避免删除正在被并发 re-reference 的对象。
// limit 控制单批大小，避免单事务扫全表持锁过久。
// ext 接受 *sqlx.DB 或 *sqlx.Tx。
func (r *FileHashRepo) ListZeroRef(ctx context.Context, ext sqlx.ExtContext, before time.Time, limit int) ([]domain.FileHash, error) {
	const q = `SELECT hash_sha256, storage_path, size, ref_count, created_at
		FROM file_hashes
		WHERE ref_count = 0 AND zero_ref_at IS NOT NULL AND zero_ref_at < $1
		ORDER BY zero_ref_at
		LIMIT $2`
	var hashes []domain.FileHash
	if err := sqlx.SelectContext(ctx, ext, &hashes, q, before, limit); err != nil {
		return nil, fmt.Errorf("list zero-ref file_hash: %w", err)
	}
	return hashes, nil
}

// DeleteIfZeroRef 删除零引用行（GC 删 MinIO 对象后调）。
// 再校验 ref_count=0：grace 窗口内若已被 Upsert 重新引用，ref_count>0，
// 此 DELETE 不影响行（affected=0），调用方据此判定竞态并跳过。
// ext 接受 *sqlx.DB 或 *sqlx.Tx。
func (r *FileHashRepo) DeleteIfZeroRef(ctx context.Context, ext sqlx.ExtContext, hash string) (bool, error) {
	const q = `DELETE FROM file_hashes WHERE hash_sha256 = $1 AND ref_count = 0`
	res, err := ext.ExecContext(ctx, q, hash)
	if err != nil {
		return false, fmt.Errorf("delete zero-ref file_hash: %w", err)
	}
	affected, _ := res.RowsAffected()
	return affected > 0, nil
}
