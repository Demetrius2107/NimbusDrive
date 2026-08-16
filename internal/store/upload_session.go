package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/Demetrius2107/NimbusDrive/internal/domain"
	"github.com/jmoiron/sqlx"
)

// UploadSessionRepo 封装 upload_sessions 表的数据访问（断点续传会话）。
// 设计文档 4.5/4.6/9：MVP 用 uploaded_chunks 位图（BYTEA），并发收块时 SELECT ... FOR UPDATE 后置位。
type UploadSessionRepo struct {
	db *sqlx.DB
}

// Create 创建上传会话。返回会话 ID。
func (r *UploadSessionRepo) Create(ctx context.Context, s *domain.UploadSession) (string, error) {
	const q = `
		INSERT INTO upload_sessions (user_id, file_id, hash_sha256, total_size, chunk_size, total_chunks, status)
		VALUES ($1, $2, $3, $4, $5, $6, 'active')
		RETURNING id`
	var id string
	err := r.db.GetContext(ctx, &id, q,
		s.UserID, s.FileID, s.HashSHA256, s.TotalSize, s.ChunkSize, s.TotalChunks,
	)
	if err != nil {
		return "", fmt.Errorf("insert upload_session: %w", err)
	}
	return id, nil
}

// Get 按 ID 查会话。
func (r *UploadSessionRepo) Get(ctx context.Context, id string) (*domain.UploadSession, error) {
	const q = `SELECT id, user_id, file_id, hash_sha256, total_size, chunk_size, total_chunks,
		uploaded_chunks, status, expires_at, created_at, updated_at
		FROM upload_sessions WHERE id = $1`
	var s domain.UploadSession
	if err := r.db.GetContext(ctx, &s, q, id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, domain.ErrNotFound
		}
		return nil, fmt.Errorf("get upload_session: %w", err)
	}
	return &s, nil
}

// MarkChunkUploaded 标记某分块已上传：行锁后位图置位。
// 设计文档第九章：SELECT ... FOR UPDATE 锁会话行后置位，防并发覆盖。
func (r *UploadSessionRepo) MarkChunkUploaded(ctx context.Context, id string, chunkIndex, totalChunks int) error {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	const lockQ = `SELECT uploaded_chunks FROM upload_sessions WHERE id = $1 FOR UPDATE`
	var bitmap []byte
	if err := tx.GetContext(ctx, &bitmap, lockQ, id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.ErrNotFound
		}
		return fmt.Errorf("lock upload_session: %w", err)
	}

	bitmap = ensureBitmapLen(bitmap, totalChunks)
	setBit(bitmap, chunkIndex)

	const updQ = `UPDATE upload_sessions SET uploaded_chunks = $2 WHERE id = $1`
	if _, err := tx.ExecContext(ctx, updQ, id, bitmap); err != nil {
		return fmt.Errorf("update bitmap: %w", err)
	}
	return tx.Commit()
}

// MissingChunks 返回尚未上传的分块索引列表（断点续传）。
func (r *UploadSessionRepo) MissingChunks(ctx context.Context, id string) ([]int, error) {
	s, err := r.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	bitmap := ensureBitmapLen(s.UploadedChunks, s.TotalChunks)
	var missing []int
	for i := 0; i < s.TotalChunks; i++ {
		if !getBit(bitmap, i) {
			missing = append(missing, i)
		}
	}
	return missing, nil
}

// Complete 标记会话完成。
func (r *UploadSessionRepo) Complete(ctx context.Context, id string) error {
	const q = `UPDATE upload_sessions SET status = 'completed' WHERE id = $1 AND status = 'active'`
	res, err := r.db.ExecContext(ctx, q, id)
	if err != nil {
		return fmt.Errorf("complete upload_session: %w", err)
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// Abort 标记会话中止。
func (r *UploadSessionRepo) Abort(ctx context.Context, id string) error {
	const q = `UPDATE upload_sessions SET status = 'aborted' WHERE id = $1 AND status = 'active'`
	res, err := r.db.ExecContext(ctx, q, id)
	if err != nil {
		return fmt.Errorf("abort upload_session: %w", err)
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// MarkExpired 批量将过期会话标记为 expired。返回受影响行数（定时任务用）。
func (r *UploadSessionRepo) MarkExpired(ctx context.Context) (int, error) {
	const q = `UPDATE upload_sessions SET status = 'expired'
		WHERE status = 'active' AND expires_at < now()`
	res, err := r.db.ExecContext(ctx, q)
	if err != nil {
		return 0, fmt.Errorf("mark expired upload_sessions: %w", err)
	}
	affected, _ := res.RowsAffected()
	return int(affected), nil
}

// --- 位图工具 ---

// ensureBitmapLen 保证位图字节切片长度足够容纳 totalChunks 个位。
func ensureBitmapLen(bitmap []byte, totalChunks int) []byte {
	need := (totalChunks + 7) / 8
	if len(bitmap) < need {
		grown := make([]byte, need)
		copy(grown, bitmap)
		return grown
	}
	return bitmap
}

func setBit(bitmap []byte, index int) {
	byteIdx := index / 8
	bitIdx := index % 8
	// 高位在前：第 0 位在最高位（0x80），便于可读性。
	bitmap[byteIdx] |= 0x80 >> bitIdx
}

func getBit(bitmap []byte, index int) bool {
	byteIdx := index / 8
	bitIdx := index % 8
	if byteIdx >= len(bitmap) {
		return false
	}
	return bitmap[byteIdx]&(0x80>>bitIdx) != 0
}
