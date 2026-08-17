// 管理后台 handler：用户管理/文件审计/操作日志查询。
// 一个业务模块一个文件：本文件为管理端（APIServer 侧）。
// 所有接口受 GinJWTAuth + GinAdminOnly 双中间件保护。
// 管理操作产生的审计日志通过 LogAggregator 异步写入，不阻塞主流程。
package handler

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/Demetrius2107/NimbusDrive/internal/adminstore"
	"github.com/Demetrius2107/NimbusDrive/internal/domain"
	"github.com/Demetrius2107/NimbusDrive/internal/middleware"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// AdminHandler 处理管理后台接口。
type AdminHandler struct {
	users *adminstore.UserRepo
	files *adminstore.FileRepo
	logs  *adminstore.LogQueryRepo
	la    *adminstore.LogAggregator
}

// NewAdminHandler 构造 AdminHandler。
func NewAdminHandler(users *adminstore.UserRepo, files *adminstore.FileRepo, logs *adminstore.LogQueryRepo, la *adminstore.LogAggregator) *AdminHandler {
	return &AdminHandler{users: users, files: files, logs: logs, la: la}
}

// --- 用户管理 ---

// ListUsers GET /api/v1/admin/users — 用户列表（分页、搜索、状态筛选）。
func (h *AdminHandler) ListUsers(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "50"))
	search := c.Query("search")
	status, _ := strconv.Atoi(c.DefaultQuery("status", "0"))

	users, total, err := h.users.ListUsers(c.Request.Context(), page, pageSize, search, int16(status))
	if err != nil {
		if errIsNotFound(err) {
			abortNotFound(c, "查询失败")
			return
		}
		abortInternal(c, "查询用户列表失败")
		return
	}

	h.logAction(c, "admin.user.list", nil, nil, nil)

	c.JSON(http.StatusOK, gin.H{
		"code":    string(domain.CodeOK),
		"message": "ok",
		"data": gin.H{
			"users":     users,
			"page":      page,
			"page_size": pageSize,
			"total":     total,
		},
	})
}

// UpdateUserStatusRequest 修改用户状态请求体。
type UpdateUserStatusRequest struct {
	Status int16 `json:"status" binding:"required,oneof=1 2"`
}

// UpdateUserStatus PATCH /api/v1/admin/users/:id/status — 封禁/解封用户。
func (h *AdminHandler) UpdateUserStatus(c *gin.Context) {
	userID, ok := parseAdminUserID(c)
	if !ok {
		return
	}

	var req UpdateUserStatusRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		abortBadRequest(c, "status 必须为 1(正常)或 2(封禁)")
		return
	}

	oldStatus, err := h.users.UpdateStatus(c.Request.Context(), userID, req.Status)
	if err != nil {
		if errIsNotFound(err) {
			abortNotFound(c, "用户不存在")
			return
		}
		abortInternal(c, "修改用户状态失败")
		return
	}

	targetID := strconv.FormatInt(userID, 10)
	detail, _ := json.Marshal(map[string]int16{"old": oldStatus, "new": req.Status})
	h.logAction(c, "admin.user.status", strPtr("user"), &targetID, detail)

	c.JSON(http.StatusOK, gin.H{"code": string(domain.CodeOK), "message": "ok"})
}

// UpdateUserQuotaRequest 修改用户配额请求体。
type UpdateUserQuotaRequest struct {
	Quota int64 `json:"quota" binding:"required,min=0"`
}

// UpdateUserQuota PATCH /api/v1/admin/users/:id/quota — 调整用户配额。
func (h *AdminHandler) UpdateUserQuota(c *gin.Context) {
	userID, ok := parseAdminUserID(c)
	if !ok {
		return
	}

	var req UpdateUserQuotaRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		abortBadRequest(c, "quota 必须为非负整数")
		return
	}

	oldQuota, err := h.users.UpdateQuota(c.Request.Context(), userID, req.Quota)
	if err != nil {
		if errIsNotFound(err) {
			abortNotFound(c, "用户不存在")
			return
		}
		abortInternal(c, "修改用户配额失败")
		return
	}

	targetID := strconv.FormatInt(userID, 10)
	detail, _ := json.Marshal(map[string]int64{"old": oldQuota, "new": req.Quota})
	h.logAction(c, "admin.user.quota", strPtr("user"), &targetID, detail)

	c.JSON(http.StatusOK, gin.H{"code": string(domain.CodeOK), "message": "ok"})
}

// --- 文件审计 ---

// ListFiles GET /api/v1/admin/files — 全局文件列表（分页、用户筛选）。
func (h *AdminHandler) ListFiles(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "50"))
	userID, _ := strconv.ParseInt(c.Query("user_id"), 10, 64)

	files, total, err := h.files.ListFiles(c.Request.Context(), page, pageSize, userID)
	if err != nil {
		abortInternal(c, "查询文件列表失败")
		return
	}

	h.logAction(c, "admin.file.list", nil, nil, nil)

	c.JSON(http.StatusOK, gin.H{
		"code":    string(domain.CodeOK),
		"message": "ok",
		"data": gin.H{
			"files":     files,
			"page":      page,
			"page_size": pageSize,
			"total":     total,
		},
	})
}

// --- 操作日志 ---

// ListLogs GET /api/v1/admin/logs — 操作日志查询（分页、按 action/actor 筛选）。
// 注意：日志查询本身不记录日志，避免递归。
func (h *AdminHandler) ListLogs(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "50"))
	action := c.Query("action")
	actorID, _ := strconv.ParseInt(c.Query("actor_id"), 10, 64)

	logs, total, err := h.logs.List(c.Request.Context(), page, pageSize, action, actorID)
	if err != nil {
		abortInternal(c, "查询操作日志失败")
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"code":    string(domain.CodeOK),
		"message": "ok",
		"data": gin.H{
			"logs":      logs,
			"page":      page,
			"page_size": pageSize,
			"total":     total,
		},
	})
}

// --- 辅助 ---

// parseAdminUserID 从路径参数解析用户 ID（int64）。
func parseAdminUserID(c *gin.Context) (int64, bool) {
	idStr := c.Param("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || id <= 0 {
		abortBadRequest(c, "用户 ID 无效")
		return 0, false
	}
	return id, true
}

// logAction 异步记录操作日志。非阻塞，失败只丢弃不报错。
func (h *AdminHandler) logAction(c *gin.Context, action string, targetType *string, targetID *string, detail json.RawMessage) {
	if h.la == nil {
		return
	}
	actorID := c.GetInt64(middleware.CtxUserID)
	ip := c.ClientIP()
	entry := adminstore.OperationLog{
		ActorID:    &actorID,
		ActorType:  "admin",
		Action:     action,
		TargetType: targetType,
		TargetID:   targetID,
		Detail:     detail,
	}
	if ip != "" {
		entry.IP = &ip
	}
	h.la.Record(entry)
}

// errIsNotFound 判断 GORM 是否为记录未找到错误。
func errIsNotFound(err error) bool {
	return err == gorm.ErrRecordNotFound
}

func strPtr(s string) *string { return &s }
