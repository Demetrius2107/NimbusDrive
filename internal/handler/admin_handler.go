// 管理后台 handler：用户管理/文件审计/操作日志查询。
// 一个业务模块一个文件：本文件为管理端（APIServer 侧）。
// 所有接口受 GinJWTAuth + GinAdminOnly 双中间件保护。
// 管理操作产生的审计日志通过 LogAggregator 异步写入，不阻塞主流程。
package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/Demetrius2107/NimbusDrive/internal/adminstore"
	"github.com/Demetrius2107/NimbusDrive/internal/domain"
	"github.com/Demetrius2107/NimbusDrive/internal/middleware"
	"github.com/Demetrius2107/NimbusDrive/internal/quota"
	"github.com/Demetrius2107/NimbusDrive/internal/logger"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// quotaNotifier 解耦 AdminHandler 与 quota 包实现，便于单测 mock。
// targetUserID=0 表示广播。返回分配的版本号。
type quotaNotifier interface {
	NotifyChange(ctx context.Context, targetUserID int64, changeType quota.ChangeType, payload map[string]any) (int64, error)
}

// AdminHandler 处理管理后台接口。
type AdminHandler struct {
	users    *adminstore.UserRepo
	files    *adminstore.FileRepo
	logs     *adminstore.LogQueryRepo
	la       *adminstore.LogAggregator
	notifier quotaNotifier
}

// NewAdminHandler 构造 AdminHandler。
// notifier 可为 nil（Redis 不可用时降级，仅不推送，不影响管理操作本身）。
func NewAdminHandler(users *adminstore.UserRepo, files *adminstore.FileRepo, logs *adminstore.LogQueryRepo, la *adminstore.LogAggregator, notifier quotaNotifier) *AdminHandler {
	return &AdminHandler{users: users, files: files, logs: logs, la: la, notifier: notifier}
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

	// 推送配额变更给目标用户的在线客户端（nil 时降级跳过）。
	h.notifyChange(c.Request.Context(), userID, quota.ChangeUserQuota, map[string]any{
		"storage_quota": req.Quota,
		"old":           oldQuota,
	})

	c.JSON(http.StatusOK, gin.H{"code": string(domain.CodeOK), "message": "ok"})
}

// ResetAllQuotaRequest 批量重置配额请求体。
type ResetAllQuotaRequest struct {
	Quota int64 `json:"quota" binding:"required,min=0"`
}

// ResetAllQuota POST /api/v1/admin/users/quota/reset — 批量重置所有普通用户存储配额。
// 场景：管理员一键把所有用户配额重置为指定值，客户端经 SSE 实时收到 reset_all 广播。
func (h *AdminHandler) ResetAllQuota(c *gin.Context) {
	var req ResetAllQuotaRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		abortBadRequest(c, "quota 必须为非负整数")
		return
	}

	affected, err := h.users.ResetAllQuota(c.Request.Context(), req.Quota)
	if err != nil {
		abortInternal(c, "批量重置配额失败")
		return
	}

	detail, _ := json.Marshal(map[string]any{"quota": req.Quota, "affected": affected})
	h.logAction(c, "admin.user.quota.reset_all", strPtr("user"), nil, detail)

	// 广播 reset_all：所有在线客户端收到后重新拉取自身配额。
	h.notifyChange(c.Request.Context(), 0, quota.ChangeResetAll, map[string]any{
		"storage_quota": req.Quota,
		"affected":      affected,
	})

	c.JSON(http.StatusOK, gin.H{
		"code":    string(domain.CodeOK),
		"message": "ok",
		"data":    gin.H{"affected": affected, "quota": req.Quota},
	})
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

// notifyChange 推送配额变更。非阻塞：推送失败只告警，不影响管理操作结果。
// 配额变更已落库，客户端即便没收到推送，下次刷新 /auth/me 或轮询也能拿到最新值。
func (h *AdminHandler) notifyChange(ctx context.Context, targetUserID int64, changeType quota.ChangeType, payload map[string]any) {
	if h.notifier == nil {
		return
	}
	if _, err := h.notifier.NotifyChange(ctx, targetUserID, changeType, payload); err != nil {
		logger.L.Warn("notify quota change failed", zap.Error(err))
	}
}

func strPtr(s string) *string { return &s }
