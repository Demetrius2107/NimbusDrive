package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/Demetrius2107/NimbusDrive/internal/domain"
	"github.com/jmoiron/sqlx"
)

// FileRepo 封装 files 表的数据访问（文件/文件夹节点，用户视图）。
type FileRepo struct {
	db *sqlx.DB
}

// fileCols 是 files 表的查询列，与 domain.FileNode 的 db tag 对齐。
const fileCols = `id, user_id, parent_id, name, size, mime_type, is_folder, hash_sha256,
	chunk_count, status, storage_path, deleted_at, created_at, updated_at`

// Create 创建文件/文件夹节点（init 状态占位）。返回新 ID。
func (r *FileRepo) Create(ctx context.Context, f *domain.FileNode) (int64, error) {
	const q = `
		INSERT INTO files (user_id, parent_id, name, size, mime_type, is_folder, hash_sha256, chunk_count, status, storage_path)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		RETURNING id`
	var id int64
	err := r.db.GetContext(ctx, &id, q,
		f.UserID, f.ParentID, f.Name, f.Size, f.MimeType, f.IsFolder,
		f.HashSHA256, f.ChunkCount, f.Status, f.StoragePath,
	)
	if err != nil {
		if isUniqueViolation(err) {
			return 0, domain.ErrConflict
		}
		return 0, fmt.Errorf("insert file: %w", err)
	}
	return id, nil
}

// GetByID 按 ID 查文件节点。
func (r *FileRepo) GetByID(ctx context.Context, id int64) (*domain.FileNode, error) {
	q := fmt.Sprintf(`SELECT %s FROM files WHERE id = $1`, fileCols)
	var f domain.FileNode
	if err := r.db.GetContext(ctx, &f, q, id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, domain.ErrNotFound
		}
		return nil, fmt.Errorf("get file by id: %w", err)
	}
	return &f, nil
}

// ListByParent 列出某用户在指定父目录下的未删除节点（分页）。
// parentID 为 nil 时列出根目录。返回节点列表与总数。
func (r *FileRepo) ListByParent(ctx context.Context, userID int64, parentID *int64, page, size int) ([]domain.FileNode, int, error) {
	if page < 1 {
		page = 1
	}
	if size < 1 || size > 200 {
		size = 50
	}
	offset := (page - 1) * size

	q := fmt.Sprintf(`
		SELECT %s FROM files
		WHERE user_id = $1 AND deleted_at IS NULL AND
		      ((parent_id IS NULL AND $2::bigint IS NULL) OR parent_id = $2)
		ORDER BY is_folder DESC, name ASC
		LIMIT $3 OFFSET $4`, fileCols)

	var nodes []domain.FileNode
	if err := r.db.SelectContext(ctx, &nodes, q, userID, parentID, size, offset); err != nil {
		return nil, 0, fmt.Errorf("list files: %w", err)
	}

	const countQ = `
		SELECT count(*) FROM files
		WHERE user_id = $1 AND deleted_at IS NULL AND
		      ((parent_id IS NULL AND $2::bigint IS NULL) OR parent_id = $2)`
	var total int
	if err := r.db.GetContext(ctx, &total, countQ, userID, parentID); err != nil {
		return nil, 0, fmt.Errorf("count files: %w", err)
	}
	return nodes, total, nil
}

// MarkCompleted 上传完成回写：状态置 completed，记录哈希、存储路径、分块数、实际大小。
// size 单独传入：上传完成前 files.size 为 0 占位，合并后写入真实字节数。
// ext 接受 *sqlx.DB 或 *sqlx.Tx，使调用方可在事务内复用此方法（executor 接口模式）。
func (r *FileRepo) MarkCompleted(ctx context.Context, ext sqlx.ExtContext, id int64, hash, storagePath string, chunkCount int, size int64) error {
	const q = `
		UPDATE files
		SET status = 'completed', hash_sha256 = $2, storage_path = $3, chunk_count = $4, size = $5
		WHERE id = $1`
	res, err := ext.ExecContext(ctx, q, id, hash, storagePath, chunkCount, size)
	if err != nil {
		return fmt.Errorf("mark file completed: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("mark file completed rows: %w", err)
	}
	if affected == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// SoftDelete 软删除（移入回收站）。
func (r *FileRepo) SoftDelete(ctx context.Context, id int64) error {
	const q = `UPDATE files SET deleted_at = now() WHERE id = $1 AND deleted_at IS NULL`
	res, err := r.db.ExecContext(ctx, q, id)
	if err != nil {
		return fmt.Errorf("soft delete file: %w", err)
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// Restore 从回收站恢复。
func (r *FileRepo) Restore(ctx context.Context, id int64) error {
	const q = `UPDATE files SET deleted_at = NULL WHERE id = $1 AND deleted_at IS NOT NULL`
	res, err := r.db.ExecContext(ctx, q, id)
	if err != nil {
		return fmt.Errorf("restore file: %w", err)
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// Move 移动节点到新父目录。
func (r *FileRepo) Move(ctx context.Context, id int64, newParentID *int64) error {
	const q = `UPDATE files SET parent_id = $2 WHERE id = $1 AND deleted_at IS NULL`
	res, err := r.db.ExecContext(ctx, q, id, newParentID)
	if err != nil {
		if isUniqueViolation(err) {
			return domain.ErrConflict
		}
		return fmt.Errorf("move file: %w", err)
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// Rename 重命名节点。
func (r *FileRepo) Rename(ctx context.Context, id int64, name string) error {
	const q = `UPDATE files SET name = $2 WHERE id = $1 AND deleted_at IS NULL`
	res, err := r.db.ExecContext(ctx, q, id, name)
	if err != nil {
		if isUniqueViolation(err) {
			return domain.ErrConflict
		}
		return fmt.Errorf("rename file: %w", err)
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// HardDelete 物理删除文件节点（取消上传时清理 init 状态占位文件用）。
func (r *FileRepo) HardDelete(ctx context.Context, id int64) error {
	const q = `DELETE FROM files WHERE id = $1`
	res, err := r.db.ExecContext(ctx, q, id)
	if err != nil {
		return fmt.Errorf("hard delete file: %w", err)
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// UpdateSize 更新文件实际大小（合并完成时用）。
func (r *FileRepo) UpdateSize(ctx context.Context, id int64, size int64) error {
	const q = `UPDATE files SET size = $2 WHERE id = $1`
	_, err := r.db.ExecContext(ctx, q, id, size)
	if err != nil {
		return fmt.Errorf("update file size: %w", err)
	}
	return nil
}

// Subtree 用递归 CTE 查询某文件夹下所有后代（含子文件夹与文件）。
// 设计文档 4.9 节子树查询。
func (r *FileRepo) Subtree(ctx context.Context, folderID int64) ([]domain.FileNode, error) {
	const q = `
		WITH RECURSIVE subtree AS (
			SELECT id, user_id, parent_id, name, size, mime_type, is_folder, hash_sha256,
			       chunk_count, status, storage_path, deleted_at, created_at, updated_at, 0 AS depth
			FROM files WHERE id = $1 AND deleted_at IS NULL
			UNION ALL
			SELECT f.id, f.user_id, f.parent_id, f.name, f.size, f.mime_type, f.is_folder, f.hash_sha256,
			       f.chunk_count, f.status, f.storage_path, f.deleted_at, f.created_at, f.updated_at, s.depth + 1
			FROM files f
			JOIN subtree s ON f.parent_id = s.id
			WHERE f.deleted_at IS NULL
		)
		SELECT id, user_id, parent_id, name, size, mime_type, is_folder, hash_sha256,
		       chunk_count, status, storage_path, deleted_at, created_at, updated_at
		FROM subtree ORDER BY depth, name`
	var nodes []domain.FileNode
	if err := r.db.SelectContext(ctx, &nodes, q, folderID); err != nil {
		return nil, fmt.Errorf("subtree query: %w", err)
	}
	return nodes, nil
}

// ListTrash 列出某用户回收站中的节点（deleted_at IS NOT NULL），分页。
// 按 deleted_at DESC 排序（最近删除的在前）。
func (r *FileRepo) ListTrash(ctx context.Context, userID int64, page, size int) ([]domain.FileNode, int, error) {
	if page < 1 {
		page = 1
	}
	if size < 1 || size > 200 {
		size = 50
	}
	offset := (page - 1) * size

	q := fmt.Sprintf(`
		SELECT %s FROM files
		WHERE user_id = $1 AND deleted_at IS NOT NULL
		ORDER BY deleted_at DESC
		LIMIT $2 OFFSET $3`, fileCols)

	var nodes []domain.FileNode
	if err := r.db.SelectContext(ctx, &nodes, q, userID, size, offset); err != nil {
		return nil, 0, fmt.Errorf("list trash: %w", err)
	}

	const countQ = `SELECT count(*) FROM files WHERE user_id = $1 AND deleted_at IS NOT NULL`
	var total int
	if err := r.db.GetContext(ctx, &total, countQ, userID); err != nil {
		return nil, 0, fmt.Errorf("count trash: %w", err)
	}
	return nodes, total, nil
}

// SoftDeleteRecursive 递归软删除：将节点及其所有后代标记为已删除（移入回收站）。
// 用递归 CTE 一次性找出子树所有 ID，批量 UPDATE，避免多次往返。
func (r *FileRepo) SoftDeleteRecursive(ctx context.Context, id int64) error {
	const q = `
		WITH RECURSIVE subtree AS (
			SELECT id FROM files WHERE id = $1 AND deleted_at IS NULL
			UNION ALL
			SELECT f.id FROM files f JOIN subtree s ON f.parent_id = s.id WHERE f.deleted_at IS NULL
		)
		UPDATE files SET deleted_at = now()
		WHERE id IN (SELECT id FROM subtree) AND deleted_at IS NULL`
	res, err := r.db.ExecContext(ctx, q, id)
	if err != nil {
		return fmt.Errorf("soft delete recursive: %w", err)
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// HardDeleteRecursive 递归物理删除：删除节点及其所有后代（含已软删除的子节点）。
// 彻底删除文件夹时用。注意：调用方需在此之后对每个文件节点做 ref_count--。
func (r *FileRepo) HardDeleteRecursive(ctx context.Context, id int64) error {
	const q = `
		WITH RECURSIVE subtree AS (
			SELECT id FROM files WHERE id = $1
			UNION ALL
			SELECT f.id FROM files f JOIN subtree s ON f.parent_id = s.id
		)
		DELETE FROM files WHERE id IN (SELECT id FROM subtree)`
	res, err := r.db.ExecContext(ctx, q, id)
	if err != nil {
		return fmt.Errorf("hard delete recursive: %w", err)
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// ListDescendants 列出某节点的所有后代（含已软删除的），用于彻底删除时收集 hash 做 ref_count--。
// 返回所有 is_folder=false 且 hash_sha256 IS NOT NULL 的后代文件。
func (r *FileRepo) ListDescendants(ctx context.Context, id int64) ([]domain.FileNode, error) {
	q := fmt.Sprintf(`
		WITH RECURSIVE subtree AS (
			SELECT %s FROM files WHERE id = $1
			UNION ALL
			SELECT f.%s FROM files f JOIN subtree s ON f.parent_id = s.id
		)
		SELECT %s FROM subtree
		WHERE is_folder = false AND hash_sha256 IS NOT NULL`, fileCols, fileCols, fileCols)
	var nodes []domain.FileNode
	if err := r.db.SelectContext(ctx, &nodes, q, id); err != nil {
		return nil, fmt.Errorf("list descendants: %w", err)
	}
	return nodes, nil
}
