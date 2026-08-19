// 文件管理 handler：列表/文件夹/移动/重命名/软删除/回收站。
// 一个业务模块一个文件：本文件为文件管理（APIServer 侧）。
package handler

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/Demetrius2107/NimbusDrive/internal/domain"
	"github.com/Demetrius2107/NimbusDrive/internal/middleware"
	"github.com/Demetrius2107/NimbusDrive/internal/store"
	"github.com/gin-gonic/gin"
	"github.com/jmoiron/sqlx"
)

// FileHandler 处理文件管理与回收站接口。
type FileHandler struct {
	files  *store.FileRepo
	hashes *store.FileHashRepo
	db     *sqlx.DB
}

// NewFileHandler 构造 FileHandler。
// db 用于彻底删除时跨表操作（files 删除 + file_hashes ref_count--）。
func NewFileHandler(files *store.FileRepo, hashes *store.FileHashRepo, db *sqlx.DB) *FileHandler {
	return &FileHandler{files: files, hashes: hashes, db: db}
}

// --- 文件管理接口 ---

// List GET /api/v1/files — 列出指定父目录下的文件/文件夹。
func (h *FileHandler) List(c *gin.Context) {
	userID := c.GetInt64(middleware.CtxUserID)

	var parentID *int64
	if pidStr := c.Query("parent_id"); pidStr != "" {
		pid, err := strconv.ParseInt(pidStr, 10, 64)
		if err != nil || pid <= 0 {
			abortBadRequest(c, "parent_id 无效")
			return
		}
		parentID = &pid
	}

	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "50"))

	nodes, total, err := h.files.ListByParent(c.Request.Context(), userID, parentID, page, pageSize)
	if err != nil {
		abortInternal(c, "查询文件列表失败")
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"code":    string(domain.CodeOK),
		"message": "ok",
		"data": gin.H{
			"files":     nodes,
			"page":      page,
			"page_size": pageSize,
			"total":     total,
		},
	})
}

// CreateFolderRequest 创建文件夹请求体。
type CreateFolderRequest struct {
	Name     string `json:"name" binding:"required,min=1,max=255"`
	ParentID *int64 `json:"parent_id"`
}

// CreateFolder POST /api/v1/files/folder — 创建文件夹。
func (h *FileHandler) CreateFolder(c *gin.Context) {
	userID := c.GetInt64(middleware.CtxUserID)

	var req CreateFolderRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		abortBadRequest(c, "参数错误: "+err.Error())
		return
	}

	folder := &domain.FileNode{
		UserID:   userID,
		ParentID: req.ParentID,
		Name:     strings.TrimSpace(req.Name),
		IsFolder: true,
		Status:   domain.FileStatusCompleted,
		MimeType: "inode/directory",
	}

	id, err := h.files.Create(c.Request.Context(), folder)
	if err != nil {
		if errors.Is(err, domain.ErrConflict) {
			abortConflict(c, "同名文件夹已存在")
			return
		}
		abortInternal(c, "创建文件夹失败")
		return
	}

	c.JSON(http.StatusCreated, gin.H{
		"code":    string(domain.CodeOK),
		"message": "ok",
		"data": gin.H{
			"id":        id,
			"name":      folder.Name,
			"parent_id": folder.ParentID,
			"is_folder": true,
		},
	})
}

// MoveRequest 移动文件请求体。
type MoveRequest struct {
	ParentID *int64 `json:"parent_id"`
}

// Move POST /api/v1/files/:id/move — 移动文件/文件夹到新父目录。
func (h *FileHandler) Move(c *gin.Context) {
	userID := c.GetInt64(middleware.CtxUserID)
	fileID, ok := parseFileID(c)
	if !ok {
		return
	}

	var req MoveRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		abortBadRequest(c, "参数错误: "+err.Error())
		return
	}

	file, err := h.files.GetByID(c.Request.Context(), fileID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			abortNotFound(c, "文件不存在")
			return
		}
		abortInternal(c, "查询文件失败")
		return
	}
	if file.UserID != userID {
		abortForbidden(c, "无权操作此文件")
		return
	}
	if file.DeletedAt != nil {
		abortNotFound(c, "文件已删除")
		return
	}

	if err := h.files.Move(c.Request.Context(), fileID, req.ParentID); err != nil {
		if errors.Is(err, domain.ErrConflict) {
			abortConflict(c, "目标目录已存在同名文件")
			return
		}
		if errors.Is(err, domain.ErrNotFound) {
			abortNotFound(c, "文件不存在")
			return
		}
		abortInternal(c, "移动文件失败")
		return
	}

	c.JSON(http.StatusOK, gin.H{"code": string(domain.CodeOK), "message": "ok"})
}

// RenameRequest 重命名请求体。
type RenameRequest struct {
	Name string `json:"name" binding:"required,min=1,max=255"`
}

// Rename POST /api/v1/files/:id/rename — 重命名文件/文件夹。
func (h *FileHandler) Rename(c *gin.Context) {
	userID := c.GetInt64(middleware.CtxUserID)
	fileID, ok := parseFileID(c)
	if !ok {
		return
	}

	var req RenameRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		abortBadRequest(c, "参数错误: "+err.Error())
		return
	}

	file, err := h.files.GetByID(c.Request.Context(), fileID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			abortNotFound(c, "文件不存在")
			return
		}
		abortInternal(c, "查询文件失败")
		return
	}
	if file.UserID != userID {
		abortForbidden(c, "无权操作此文件")
		return
	}
	if file.DeletedAt != nil {
		abortNotFound(c, "文件已删除")
		return
	}

	if err := h.files.Rename(c.Request.Context(), fileID, strings.TrimSpace(req.Name)); err != nil {
		if errors.Is(err, domain.ErrConflict) {
			abortConflict(c, "同名文件已存在")
			return
		}
		if errors.Is(err, domain.ErrNotFound) {
			abortNotFound(c, "文件不存在")
			return
		}
		abortInternal(c, "重命名失败")
		return
	}

	c.JSON(http.StatusOK, gin.H{"code": string(domain.CodeOK), "message": "ok"})
}

// SoftDelete DELETE /api/v1/files/:id — 软删除（移入回收站）。
// 文件夹递归软删除所有后代。
func (h *FileHandler) SoftDelete(c *gin.Context) {
	userID := c.GetInt64(middleware.CtxUserID)
	fileID, ok := parseFileID(c)
	if !ok {
		return
	}

	file, err := h.files.GetByID(c.Request.Context(), fileID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			abortNotFound(c, "文件不存在")
			return
		}
		abortInternal(c, "查询文件失败")
		return
	}
	if file.UserID != userID {
		abortForbidden(c, "无权操作此文件")
		return
	}
	if file.DeletedAt != nil {
		abortNotFound(c, "文件已删除")
		return
	}

	if file.IsFolder {
		err = h.files.SoftDeleteRecursive(c.Request.Context(), fileID)
	} else {
		err = h.files.SoftDelete(c.Request.Context(), fileID)
	}
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			abortNotFound(c, "文件不存在")
			return
		}
		abortInternal(c, "删除失败")
		return
	}

	c.JSON(http.StatusOK, gin.H{"code": string(domain.CodeOK), "message": "ok"})
}

// --- 回收站接口 ---

// ListTrash GET /api/v1/trash — 列出回收站。
func (h *FileHandler) ListTrash(c *gin.Context) {
	userID := c.GetInt64(middleware.CtxUserID)

	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "50"))

	nodes, total, err := h.files.ListTrash(c.Request.Context(), userID, page, pageSize)
	if err != nil {
		abortInternal(c, "查询回收站失败")
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"code":    string(domain.CodeOK),
		"message": "ok",
		"data": gin.H{
			"files":     nodes,
			"page":      page,
			"page_size": pageSize,
			"total":     total,
		},
	})
}

// Restore POST /api/v1/trash/:id/restore — 从回收站恢复。
// MVP 只恢复单节点；文件夹的子节点需用户手动逐个恢复。
func (h *FileHandler) Restore(c *gin.Context) {
	userID := c.GetInt64(middleware.CtxUserID)
	fileID, ok := parseFileID(c)
	if !ok {
		return
	}

	file, err := h.files.GetByID(c.Request.Context(), fileID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			abortNotFound(c, "文件不存在")
			return
		}
		abortInternal(c, "查询文件失败")
		return
	}
	if file.UserID != userID {
		abortForbidden(c, "无权操作此文件")
		return
	}
	if file.DeletedAt == nil {
		abortBadRequest(c, "文件不在回收站中")
		return
	}

	if err := h.files.Restore(c.Request.Context(), fileID); err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			abortNotFound(c, "文件不在回收站中")
			return
		}
		abortInternal(c, "恢复失败")
		return
	}

	c.JSON(http.StatusOK, gin.H{"code": string(domain.CodeOK), "message": "ok"})
}

// PermanentDelete DELETE /api/v1/trash/:id — 彻底删除。
// 文件：HardDelete + file_hashes ref_count--；文件夹：HardDeleteRecursive + 对每个文件 ref_count--。
func (h *FileHandler) PermanentDelete(c *gin.Context) {
	userID := c.GetInt64(middleware.CtxUserID)
	fileID, ok := parseFileID(c)
	if !ok {
		return
	}

	file, err := h.files.GetByID(c.Request.Context(), fileID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			abortNotFound(c, "文件不存在")
			return
		}
		abortInternal(c, "查询文件失败")
		return
	}
	if file.UserID != userID {
		abortForbidden(c, "无权操作此文件")
		return
	}
	if file.DeletedAt == nil {
		abortBadRequest(c, "文件不在回收站中")
		return
	}

	// 收集所有需要 ref_count-- 的文件 hash（含文件夹后代）。
	descendants, err := h.files.ListDescendants(c.Request.Context(), fileID)
	if err != nil {
		abortInternal(c, "查询文件子树失败")
		return
	}

	// 物理删除。
	if file.IsFolder {
		err = h.files.HardDeleteRecursive(c.Request.Context(), fileID)
	} else {
		err = h.files.HardDelete(c.Request.Context(), fileID)
	}
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			abortNotFound(c, "文件不存在")
			return
		}
		abortInternal(c, "彻底删除失败")
		return
	}

	// 对每个物理文件的 hash 做 ref_count--（非阻塞，失败只记录不影响主流程）。
	for _, desc := range descendants {
		if desc.HashSHA256 != nil && *desc.HashSHA256 != "" {
			_ = h.hashes.DecrRef(c.Request.Context(), h.db, *desc.HashSHA256)
		}
	}

	c.JSON(http.StatusOK, gin.H{"code": string(domain.CodeOK), "message": "ok"})
}

// --- 辅助 ---

// parseFileID 从路径参数解析文件 ID，失败时写入错误响应。
func parseFileID(c *gin.Context) (int64, bool) {
	idStr := c.Param("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || id <= 0 {
		abortBadRequest(c, "文件 ID 无效")
		return 0, false
	}
	return id, true
}
