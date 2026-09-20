// Package handler — 应用专用密码管理（WebDAV 等协议客户端凭据）。
// 设计：docs/protocol-specs/webdav.md 决策 D2。
// 明文密码仅创建响应返回一次，库中只存 bcrypt；吊销保留行作审计痕迹。
package handler

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net/http"
	"time"

	"github.com/Demetrius2107/NimbusDrive/internal/domain"
	"github.com/Demetrius2107/NimbusDrive/internal/middleware"
	"github.com/Demetrius2107/NimbusDrive/internal/store"
	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"
)

// AppPasswordHandler 管理应用专用密码。
type AppPasswordHandler struct {
	passwords *store.AppPasswordRepo
}

// NewAppPasswordHandler 构造 AppPasswordHandler。
func NewAppPasswordHandler(passwords *store.AppPasswordRepo) *AppPasswordHandler {
	return &AppPasswordHandler{passwords: passwords}
}

// CreateAppPasswordRequest 创建请求体。
type CreateAppPasswordRequest struct {
	Name string `json:"name" binding:"required,min=1,max=64"`
}

// Create POST /api/v1/auth/app-passwords
// 生成 16 字节随机密码（base64url 无填充，22 字符），bcrypt 入库，明文仅此一次返回。
func (h *AppPasswordHandler) Create(c *gin.Context) {
	var req CreateAppPasswordRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		abortBadRequest(c, "参数错误: "+err.Error())
		return
	}
	userID := c.GetInt64(middleware.CtxUserID)

	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		abortInternal(c, "生成密码失败")
		return
	}
	plaintext := base64.RawURLEncoding.EncodeToString(raw)

	hash, err := bcrypt.GenerateFromPassword([]byte(plaintext), bcrypt.DefaultCost)
	if err != nil {
		abortInternal(c, "密码哈希失败")
		return
	}

	id, err := h.passwords.Create(c.Request.Context(), userID, req.Name, string(hash))
	if err != nil {
		abortInternal(c, "保存应用密码失败")
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"code":    string(domain.CodeOK),
		"message": "ok",
		"data": gin.H{
			"id":        id,
			"name":      req.Name,
			"password":  plaintext, // 仅此一次返回
			"note":      "请立即保存该密码，关闭本响应后无法再次查看",
			"created_at": time.Now().UTC().Format(time.RFC3339),
		},
	})
}

// List GET /api/v1/auth/app-passwords
// 返回全部（含已吊销）；不返回密码哈希。
func (h *AppPasswordHandler) List(c *gin.Context) {
	userID := c.GetInt64(middleware.CtxUserID)
	list, err := h.passwords.ListByUser(c.Request.Context(), userID)
	if err != nil {
		abortInternal(c, "查询应用密码失败")
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"code":    string(domain.CodeOK),
		"message": "ok",
		"data":    list,
	})
}

// Revoke DELETE /api/v1/auth/app-passwords/:id
func (h *AppPasswordHandler) Revoke(c *gin.Context) {
	userID := c.GetInt64(middleware.CtxUserID)
	var uri struct {
		ID int64 `uri:"id" binding:"required,min=1"`
	}
	if err := c.ShouldBindUri(&uri); err != nil {
		abortBadRequest(c, "参数错误: "+err.Error())
		return
	}
	err := h.passwords.Revoke(c.Request.Context(), userID, uri.ID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			abortNotFound(c, "应用密码不存在")
			return
		}
		abortInternal(c, "吊销应用密码失败")
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"code":    string(domain.CodeOK),
		"message": "ok",
		"data":    nil,
	})
}
