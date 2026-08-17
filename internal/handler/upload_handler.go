package handler

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Demetrius2107/NimbusDrive/internal/domain"
	"github.com/Demetrius2107/NimbusDrive/internal/eventbus"
	"github.com/Demetrius2107/NimbusDrive/internal/middleware"
	"github.com/Demetrius2107/NimbusDrive/internal/storage"
	"github.com/Demetrius2107/NimbusDrive/internal/store"
	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/common/utils"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/minio/minio-go/v7"
)

// 分块上传常量（与设计文档对齐）。
const (
	chunkSize      = 4 * 1024 * 1024 // 4MB
	maxFileNameLen = 255
)

// UploadHandler 处理 TransferServer 的上传相关接口。
type UploadHandler struct {
	repos   *store.Repositories
	mc      *storage.MinIO
	db      *sqlx.DB
	emitter *eventbus.Emitter // 可为 nil（Redis 不可用时降级）
}

// NewUploadHandler 构造 UploadHandler。
// db 用于 complete 时的跨表事务（file_hashes + files + users）。
// emitter 用于上传完成后发射 file.uploaded 事件，可为 nil。
func NewUploadHandler(repos *store.Repositories, mc *storage.MinIO, db *sqlx.DB, emitter *eventbus.Emitter) *UploadHandler {
	return &UploadHandler{repos: repos, mc: mc, db: db, emitter: emitter}
}

// CheckHashRequest 秒传判定 / 创建上传会话请求体。
type CheckHashRequest struct {
	HashSHA256 string `json:"hash_sha256" vd:"len($) == 64"`
	Size       int64  `json:"size" vd:"$ > 0"`
	Name       string `json:"name" vd:"len($) > 0"`
	ParentID   *int64 `json:"parent_id"`
}

// CheckHash POST /api/v1/upload/check-hash
// 命中哈希池 → 秒传；未命中 → 创建文件占位 + 上传会话 + MinIO Multipart。
func (h *UploadHandler) CheckHash(ctx context.Context, c *app.RequestContext) {
	var req CheckHashRequest
	if err := c.BindAndValidate(&req); err != nil {
		hertzBadRequest(c, "参数错误: "+err.Error())
		return
	}
	if len(req.Name) > maxFileNameLen {
		req.Name = req.Name[:maxFileNameLen]
	}

	userID := middleware.HertzUserID(c)

	// 1. 查哈希池：命中则秒传。
	existing, err := h.repos.Hashes.Get(ctx, req.HashSHA256)
	if err != nil && !errors.Is(err, domain.ErrNotFound) {
		hertzInternal(c, "查询哈希池失败")
		return
	}
	if existing != nil {
		fileID, err := h.instantUpload(ctx, userID, req, existing.StoragePath)
		if err != nil {
			if errors.Is(err, domain.ErrQuotaExceeded) {
				hertzJSON(c, consts.StatusRequestEntityTooLarge, domain.CodeQuotaExceeded, "配额超限", nil)
				return
			}
			hertzInternal(c, "秒传失败")
			return
		}
		// 秒传完成：发射 file.uploaded 事件（instant=true）
		var parentID any
		if req.ParentID != nil {
			parentID = *req.ParentID
		}
		h.emitFileUploaded(ctx, fileID, userID, req.HashSHA256, req.Size, existing.StoragePath, req.Name, parentID, true)
		c.JSON(consts.StatusOK, utils.H{
			"code":    string(domain.CodeOK),
			"message": "ok",
			"data":    utils.H{"instant": true, "file_id": fileID, "hash_sha256": req.HashSHA256},
		})
		return
	}

	// 2. 未命中：先做配额预检（避免无意义创建会话）。
	if err := h.precheckQuota(ctx, userID, req.Size); err != nil {
		if errors.Is(err, domain.ErrQuotaExceeded) {
			hertzJSON(c, consts.StatusRequestEntityTooLarge, domain.CodeQuotaExceeded, "配额超限", nil)
			return
		}
		hertzInternal(c, "查询配额失败")
		return
	}

	// 创建文件占位记录（init 状态）。
	totalChunks := int((req.Size + int64(chunkSize) - 1) / int64(chunkSize))
	mimeType := detectMIME(req.Name)
	fileNode := &domain.FileNode{
		UserID:     userID,
		ParentID:   req.ParentID,
		Name:       req.Name,
		Size:       req.Size,
		MimeType:   mimeType,
		IsFolder:   false,
		Status:     domain.FileStatusInit,
		ChunkCount: totalChunks,
	}
	fileID, err := h.repos.Files.Create(ctx, fileNode)
	if err != nil {
		if errors.Is(err, domain.ErrConflict) {
			hertzConflict(c, "同名文件已存在")
			return
		}
		hertzInternal(c, "创建文件记录失败")
		return
	}

	// 创建 MinIO Multipart Upload。
	objectKey := storage.ObjectKey(req.HashSHA256)
	uploadID, err := h.mc.CreateMultipartUpload(ctx, objectKey)
	if err != nil {
		_ = h.repos.Files.HardDelete(ctx, fileID)
		hertzInternal(c, "创建分块上传失败")
		return
	}

	// 创建上传会话。
	session := &domain.UploadSession{
		UserID:      userID,
		FileID:      fileID,
		HashSHA256:  req.HashSHA256,
		TotalSize:   req.Size,
		ChunkSize:   chunkSize,
		TotalChunks: totalChunks,
		UploadID:    uploadID,
	}
	sessionID, err := h.repos.Uploads.Create(ctx, session)
	if err != nil {
		_ = h.mc.AbortMultipartUpload(ctx, objectKey, uploadID)
		_ = h.repos.Files.HardDelete(ctx, fileID)
		hertzInternal(c, "创建上传会话失败")
		return
	}

	c.JSON(consts.StatusOK, utils.H{
		"code":    string(domain.CodeOK),
		"message": "ok",
		"data": utils.H{
			"instant":      false,
			"session_id":   sessionID,
			"file_id":      fileID,
			"chunk_size":   chunkSize,
			"total_chunks": totalChunks,
		},
	})
}

// UploadChunk PUT /api/v1/upload/:sessionId/chunks/:index
// 接收单个分块，流式写入 MinIO，置位会话位图。
func (h *UploadHandler) UploadChunk(ctx context.Context, c *app.RequestContext) {
	sessionID := c.Param("sessionId")
	index := c.Param("index")

	idx, err := strconv.Atoi(index)
	if err != nil || idx < 0 {
		hertzBadRequest(c, "分块索引无效")
		return
	}

	userID := middleware.HertzUserID(c)

	session, err := h.repos.Uploads.Get(ctx, sessionID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			hertzNotFound(c, "上传会话不存在")
			return
		}
		hertzInternal(c, "查询上传会话失败")
		return
	}
	if session.UserID != userID {
		hertzForbidden(c, "无权操作此上传会话")
		return
	}
	if session.Status != domain.UploadSessionActive {
		hertzBadRequest(c, "上传会话状态非 active: "+string(session.Status))
		return
	}
	if idx >= session.TotalChunks {
		hertzBadRequest(c, fmt.Sprintf("分块索引越界（0-%d）", session.TotalChunks-1))
		return
	}

	// 读取分块数据（单块 ≤ 4MB+，可接受全量读入内存）。
	body := c.Request.BodyStream()
	chunk, err := io.ReadAll(body)
	if err != nil {
		hertzInternal(c, "读取分块数据失败")
		return
	}
	if len(chunk) == 0 {
		hertzBadRequest(c, "分块数据为空")
		return
	}

	objectKey := storage.ObjectKey(session.HashSHA256)
	etag, err := h.mc.UploadPart(ctx, objectKey, session.UploadID, idx+1, bytes.NewReader(chunk), int64(len(chunk)))
	if err != nil {
		hertzInternal(c, "上传分块到对象存储失败")
		return
	}

	if err := h.repos.Uploads.MarkChunkUploaded(ctx, sessionID, idx, session.TotalChunks); err != nil {
		hertzInternal(c, "更新上传进度失败")
		return
	}

	c.JSON(consts.StatusOK, utils.H{
		"code":    string(domain.CodeOK),
		"message": "ok",
		"data":    utils.H{"etag": etag, "index": idx},
	})
}

// GetUploadStatus GET /api/v1/upload/:sessionId
// 返回缺失分块列表（断点续传）。
func (h *UploadHandler) GetUploadStatus(ctx context.Context, c *app.RequestContext) {
	sessionID := c.Param("sessionId")
	userID := middleware.HertzUserID(c)

	session, err := h.repos.Uploads.Get(ctx, sessionID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			hertzNotFound(c, "上传会话不存在")
			return
		}
		hertzInternal(c, "查询上传会话失败")
		return
	}
	if session.UserID != userID {
		hertzForbidden(c, "无权操作此上传会话")
		return
	}

	missing, err := h.repos.Uploads.MissingChunks(ctx, sessionID)
	if err != nil {
		hertzInternal(c, "查询缺失分块失败")
		return
	}

	c.JSON(consts.StatusOK, utils.H{
		"code":    string(domain.CodeOK),
		"message": "ok",
		"data": utils.H{
			"session_id":   sessionID,
			"total_chunks": session.TotalChunks,
			"missing":      missing,
			"status":       session.Status,
		},
	})
}

// CompleteUpload POST /api/v1/upload/:sessionId/complete
// 合并分块 + 事务写元数据 + 配额扣减。
func (h *UploadHandler) CompleteUpload(ctx context.Context, c *app.RequestContext) {
	sessionID := c.Param("sessionId")
	userID := middleware.HertzUserID(c)

	session, err := h.repos.Uploads.Get(ctx, sessionID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			hertzNotFound(c, "上传会话不存在")
			return
		}
		hertzInternal(c, "查询上传会话失败")
		return
	}
	if session.UserID != userID {
		hertzForbidden(c, "无权操作此上传会话")
		return
	}
	if session.Status != domain.UploadSessionActive {
		hertzBadRequest(c, "上传会话状态非 active: "+string(session.Status))
		return
	}

	// 确认所有分块已上传。
	missing, err := h.repos.Uploads.MissingChunks(ctx, sessionID)
	if err != nil {
		hertzInternal(c, "查询缺失分块失败")
		return
	}
	if len(missing) > 0 {
		hertzBadRequest(c, fmt.Sprintf("尚有 %d 个分块未上传", len(missing)))
		return
	}

	objectKey := storage.ObjectKey(session.HashSHA256)

	// 从 MinIO 列出已上传 parts，组装 CompletePart 列表。
	parts, err := h.mc.ListParts(ctx, objectKey, session.UploadID)
	if err != nil {
		hertzInternal(c, "列出已上传分块失败")
		return
	}
	completeParts := make([]minio.CompletePart, 0, len(parts))
	for _, p := range parts {
		completeParts = append(completeParts, minio.CompletePart{
			PartNumber: p.PartNumber,
			ETag:       p.ETag,
		})
	}

	// 合并分块。
	if err := h.mc.CompleteMultipartUpload(ctx, objectKey, session.UploadID, completeParts); err != nil {
		hertzInternal(c, "合并分块失败")
		return
	}

	// 事务：file_hashes upsert + files MarkCompleted + users 配额扣减。
	if err := h.completeTransaction(ctx, session, objectKey); err != nil {
		if errors.Is(err, domain.ErrQuotaExceeded) {
			hertzJSON(c, consts.StatusRequestEntityTooLarge, domain.CodeQuotaExceeded, "配额超限，已回滚", nil)
			return
		}
		hertzInternal(c, "写元数据事务失败")
		return
	}

	// 标记会话完成（元数据已落库，会话状态不一致不致命）。
	_ = h.repos.Uploads.Complete(ctx, sessionID)

	// 事务提交后查 files 表补全 name/parent_id，发射 file.uploaded 事件。
	var parentID any
	if file, ferr := h.repos.Files.GetByID(ctx, session.FileID); ferr == nil {
		parentID = file.ParentID
		h.emitFileUploaded(ctx, session.FileID, session.UserID, session.HashSHA256, session.TotalSize, objectKey, file.Name, parentID, false)
	} else {
		// 查询失败仍发事件，载荷缺 name/parent_id（消费者容忍缺字段）
		h.emitFileUploaded(ctx, session.FileID, session.UserID, session.HashSHA256, session.TotalSize, objectKey, "", parentID, false)
	}

	c.JSON(consts.StatusOK, utils.H{
		"code":    string(domain.CodeOK),
		"message": "ok",
		"data": utils.H{
			"file_id":      session.FileID,
			"storage_path": objectKey,
			"hash_sha256":  session.HashSHA256,
			"size":         session.TotalSize,
		},
	})
}

// CancelUpload DELETE /api/v1/upload/:sessionId
// 取消上传：Abort MinIO + Abort 会话 + 删除 init 占位文件。
func (h *UploadHandler) CancelUpload(ctx context.Context, c *app.RequestContext) {
	sessionID := c.Param("sessionId")
	userID := middleware.HertzUserID(c)

	session, err := h.repos.Uploads.Get(ctx, sessionID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			hertzNotFound(c, "上传会话不存在")
			return
		}
		hertzInternal(c, "查询上传会话失败")
		return
	}
	if session.UserID != userID {
		hertzForbidden(c, "无权操作此上传会话")
		return
	}

	objectKey := storage.ObjectKey(session.HashSHA256)
	_ = h.mc.AbortMultipartUpload(ctx, objectKey, session.UploadID)
	_ = h.repos.Uploads.Abort(ctx, sessionID)
	_ = h.repos.Files.HardDelete(ctx, session.FileID)

	c.JSON(consts.StatusOK, utils.H{
		"code":    string(domain.CodeOK),
		"message": "ok",
	})
}

// --- 内部辅助 ---

// instantUpload 秒传：复用物理存储，新建 files 记录 + ref_count++ + 配额扣减。
// 返回新创建的 file_id。
func (h *UploadHandler) instantUpload(ctx context.Context, userID int64, req CheckHashRequest, storagePath string) (int64, error) {
	var fileID int64
	err := h.withTx(ctx, func(tx *sqlx.Tx) error {
		// ref_count++（原子）。
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO file_hashes (hash_sha256, storage_path, size, ref_count)
			VALUES ($1, $2, $3, 1)
			ON CONFLICT (hash_sha256) DO UPDATE SET ref_count = file_hashes.ref_count + 1`,
			req.HashSHA256, storagePath, req.Size); err != nil {
			return fmt.Errorf("upsert file_hash: %w", err)
		}
		// 新建 files 记录（completed）。
		if err := tx.GetContext(ctx, &fileID, `
			INSERT INTO files (user_id, parent_id, name, size, mime_type, is_folder, hash_sha256, chunk_count, status, storage_path)
			VALUES ($1, $2, $3, $4, $5, false, $6, 0, 'completed', $7)
			RETURNING id`,
			userID, req.ParentID, req.Name, req.Size, detectMIME(req.Name), req.HashSHA256, storagePath); err != nil {
			return fmt.Errorf("insert file: %w", err)
		}
		// 配额扣减（原子条件更新）。
		res, err := tx.ExecContext(ctx,
			`UPDATE users SET used_storage = used_storage + $2 WHERE id = $1 AND used_storage + $2 <= storage_quota`,
			userID, req.Size)
		if err != nil {
			return fmt.Errorf("incr used_storage: %w", err)
		}
		affected, _ := res.RowsAffected()
		if affected == 0 {
			return domain.ErrQuotaExceeded
		}
		return nil
	})
	return fileID, err
}

// completeTransaction 合并完成后的元数据事务。
func (h *UploadHandler) completeTransaction(ctx context.Context, session *domain.UploadSession, storagePath string) error {
	return h.withTx(ctx, func(tx *sqlx.Tx) error {
		// file_hashes upsert（ref_count++）。
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO file_hashes (hash_sha256, storage_path, size, ref_count)
			VALUES ($1, $2, $3, 1)
			ON CONFLICT (hash_sha256) DO UPDATE SET ref_count = file_hashes.ref_count + 1`,
			session.HashSHA256, storagePath, session.TotalSize); err != nil {
			return fmt.Errorf("upsert file_hash: %w", err)
		}
		// files 标记 completed。
		if _, err := tx.ExecContext(ctx, `
			UPDATE files SET status = 'completed', hash_sha256 = $2, storage_path = $3, chunk_count = $4, size = $5
			WHERE id = $1`,
			session.FileID, session.HashSHA256, storagePath, session.TotalChunks, session.TotalSize); err != nil {
			return fmt.Errorf("mark file completed: %w", err)
		}
		// 配额扣减。
		res, err := tx.ExecContext(ctx,
			`UPDATE users SET used_storage = used_storage + $2 WHERE id = $1 AND used_storage + $2 <= storage_quota`,
			session.UserID, session.TotalSize)
		if err != nil {
			return fmt.Errorf("incr used_storage: %w", err)
		}
		affected, _ := res.RowsAffected()
		if affected == 0 {
			return domain.ErrQuotaExceeded
		}
		return nil
	})
}

// precheckQuota 配额预检（非原子，仅避免无意义创建会话；真正扣减在 complete 事务）。
func (h *UploadHandler) precheckQuota(ctx context.Context, userID, size int64) error {
	var quota, used int64
	err := h.db.QueryRowxContext(ctx,
		`SELECT storage_quota, used_storage FROM users WHERE id = $1`, userID).Scan(&quota, &used)
	if err != nil {
		return fmt.Errorf("query quota: %w", err)
	}
	if used+size > quota {
		return domain.ErrQuotaExceeded
	}
	return nil
}

// withTx 包裹事务执行，出错自动回滚。
func (h *UploadHandler) withTx(ctx context.Context, fn func(*sqlx.Tx) error) error {
	tx, err := h.db.BeginTxx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

// --- 工具函数 ---

func detectMIME(name string) string {
	ext := strings.ToLower(filepath.Ext(name))
	switch ext {
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	case ".gif":
		return "image/gif"
	case ".mp4":
		return "video/mp4"
	case ".mp3":
		return "audio/mpeg"
	case ".pdf":
		return "application/pdf"
	case ".zip":
		return "application/zip"
	case ".txt":
		return "text/plain"
	default:
		return "application/octet-stream"
	}
}

// --- 响应辅助 ---

func hertzBadRequest(c *app.RequestContext, msg string) {
	c.JSON(consts.StatusBadRequest, utils.H{"code": string(domain.CodeInvalidParam), "message": msg})
}

func hertzNotFound(c *app.RequestContext, msg string) {
	c.JSON(consts.StatusNotFound, utils.H{"code": string(domain.CodeNotFound), "message": msg})
}

// emitFileUploaded 发射 file.uploaded 事件。emitter 为 nil 或 channel 满时静默降级。
// parentID 传 *int64 或 nil；instant 区分秒传与分块合并。
func (h *UploadHandler) emitFileUploaded(ctx context.Context, fileID, userID int64, hash string, size int64, storagePath, name string, parentID any, instant bool) {
	if h.emitter == nil {
		return
	}
	payload := map[string]any{
		"file_id":      fileID,
		"hash_sha256":  hash,
		"size":         size,
		"storage_path": storagePath,
		"name":         name,
		"instant":      instant,
	}
	if parentID != nil {
		payload["parent_id"] = parentID
	}
	h.emitter.Emit(&domain.Event{
		ID:         uuid.NewString(),
		Type:       domain.EventFileUploaded,
		OccurredAt: time.Now().UTC(),
		ActorID:    userID,
		Payload:    payload,
	})
}

func hertzForbidden(c *app.RequestContext, msg string) {
	c.JSON(consts.StatusForbidden, utils.H{"code": string(domain.CodeForbidden), "message": msg})
}

func hertzConflict(c *app.RequestContext, msg string) {
	c.JSON(consts.StatusConflict, utils.H{"code": string(domain.CodeConflict), "message": msg})
}

func hertzInternal(c *app.RequestContext, msg string) {
	c.JSON(consts.StatusInternalServerError, utils.H{"code": string(domain.CodeInternal), "message": msg})
}

func hertzJSON(c *app.RequestContext, status int, code domain.ErrorCode, msg string, data interface{}) {
	c.JSON(status, utils.H{"code": string(code), "message": msg, "data": data})
}
