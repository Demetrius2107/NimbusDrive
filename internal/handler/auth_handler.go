// Package handler 实现 APIServer 的 HTTP handler，依赖 store 与 auth。
// 一个业务模块一个文件：本文件为鉴权（register/login/me）。
package handler

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/Demetrius2107/NimbusDrive/internal/auth"
	"github.com/Demetrius2107/NimbusDrive/internal/domain"
	"github.com/Demetrius2107/NimbusDrive/internal/eventbus"
	"github.com/Demetrius2107/NimbusDrive/internal/middleware"
	"github.com/Demetrius2107/NimbusDrive/internal/store"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

// AuthHandler 处理鉴权相关接口。
type AuthHandler struct {
	users   *store.UserRepo
	jwt     *auth.JWTManager
	emitter *eventbus.Emitter // 可为 nil
}

// NewAuthHandler 构造 AuthHandler。
func NewAuthHandler(users *store.UserRepo, jwt *auth.JWTManager, emitter *eventbus.Emitter) *AuthHandler {
	return &AuthHandler{users: users, jwt: jwt, emitter: emitter}
}

// RegisterRequest 注册请求体。
type RegisterRequest struct {
	Username string `json:"username" binding:"required,min=3,max=64"`
	Email    string `json:"email" binding:"required,email,max=255"`
	Password string `json:"password" binding:"required,min=8,max=128"`
}

// Register POST /auth/register
func (h *AuthHandler) Register(c *gin.Context) {
	var req RegisterRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		abortBadRequest(c, "参数错误: "+err.Error())
		return
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		abortInternal(c, "密码哈希失败")
		return
	}

	user, err := h.users.Create(c.Request.Context(), req.Username, req.Email, string(hash))
	if err != nil {
		if errors.Is(err, domain.ErrConflict) {
			abortConflict(c, "用户名或邮箱已存在")
			return
		}
		abortInternal(c, "创建用户失败")
		return
	}
	// 发射 user.registered 事件
	if h.emitter != nil {
		h.emitter.Emit(c.Request.Context(), &domain.Event{
			ID:         uuid.NewString(),
			Type:       domain.EventUserRegistered,
			OccurredAt: time.Now().UTC(),
			ActorID:    user.ID,
			Payload: map[string]any{
				"user_id":  user.ID,
				"username": user.Username,
				"email":    user.Email,
			},
		})
	}
	c.JSON(http.StatusCreated, gin.H{
		"code":    string(domain.CodeOK),
		"message": "ok",
		"data": gin.H{
			"id":       user.ID,
			"username": user.Username,
			"email":    user.Email,
		},
	})
}

// LoginRequest 登录请求体。
type LoginRequest struct {
	Username string `json:"username" binding:"required"`
	Password string `json:"password" binding:"required"`
}

// Login POST /auth/login
func (h *AuthHandler) Login(c *gin.Context) {
	var req LoginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		abortBadRequest(c, "参数错误: "+err.Error())
		return
	}

	user, err := h.users.GetByUsername(c.Request.Context(), strings.TrimSpace(req.Username))
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			abortUnauthorized(c, "用户名或密码错误")
			return
		}
		abortInternal(c, "查询用户失败")
		return
	}

	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(req.Password)); err != nil {
		abortUnauthorized(c, "用户名或密码错误")
		return
	}

	if user.Status != 1 {
		abortForbidden(c, "账号已被禁用")
		return
	}

	token, err := h.jwt.IssueAccessToken(user.ID, user.Username, user.IsAdmin)
	if err != nil {
		abortInternal(c, "签发令牌失败")
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"code":    string(domain.CodeOK),
		"message": "ok",
		"data": gin.H{
			"token":    token,
			"id":       user.ID,
			"username": user.Username,
			"is_admin": user.IsAdmin,
		},
	})
}

// Me GET /auth/me —— 需经 GinJWTAuth 保护。
func (h *AuthHandler) Me(c *gin.Context) {
	userID := c.GetInt64(middleware.CtxUserID)
	user, err := h.users.GetByID(c.Request.Context(), userID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			abortUnauthorized(c, "用户不存在")
			return
		}
		abortInternal(c, "查询用户失败")
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"code":    string(domain.CodeOK),
		"message": "ok",
		"data": gin.H{
			"id":            user.ID,
			"username":      user.Username,
			"email":         user.Email,
			"storage_quota": user.StorageQuota,
			"used_storage":  user.UsedStorage,
			"is_admin":      user.IsAdmin,
			"created_at":    user.CreatedAt,
		},
	})
}

// --- 响应辅助 ---

func abortBadRequest(c *gin.Context, msg string) {
	c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"code": string(domain.CodeInvalidParam), "message": msg})
}

func abortUnauthorized(c *gin.Context, msg string) {
	c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"code": string(domain.CodeUnauthorized), "message": msg})
}

func abortForbidden(c *gin.Context, msg string) {
	c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"code": string(domain.CodeForbidden), "message": msg})
}

func abortConflict(c *gin.Context, msg string) {
	c.AbortWithStatusJSON(http.StatusConflict, gin.H{"code": string(domain.CodeConflict), "message": msg})
}

func abortNotFound(c *gin.Context, msg string) {
	c.AbortWithStatusJSON(http.StatusNotFound, gin.H{"code": string(domain.CodeNotFound), "message": msg})
}

func abortInternal(c *gin.Context, msg string) {
	c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"code": string(domain.CodeInternal), "message": msg})
}
