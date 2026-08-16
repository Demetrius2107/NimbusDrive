package middleware

import (
	"net/http"

	"github.com/Demetrius2107/NimbusDrive/internal/auth"
	"github.com/Demetrius2107/NimbusDrive/internal/domain"
	"github.com/Demetrius2107/NimbusDrive/internal/logger"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// 上下文键。
const (
	CtxUserID   = "user_id"
	CtxUsername = "username"
	CtxIsAdmin  = "is_admin"
)

// GinJWTAuth JWT 鉴权中间件（Gin 适配）。
// 从 Authorization: Bearer <token> 解析 → 注入 user_id/username/is_admin → 失败返回 401。
func GinJWTAuth(mgr *auth.JWTManager) gin.HandlerFunc {
	return func(c *gin.Context) {
		header := c.GetHeader("Authorization")
		tokenStr, ok := auth.ExtractBearer(header)
		if !ok {
			abortUnauthorized(c, "缺少认证令牌")
			return
		}
		claims, err := mgr.Parse(tokenStr)
		if err != nil {
			logger.L.Debug("jwt parse failed", zap.Error(err), zap.String("request_id", c.GetString("request_id")))
			abortUnauthorized(c, "认证令牌无效或已过期")
			return
		}
		c.Set(CtxUserID, claims.UserID)
		c.Set(CtxUsername, claims.Username)
		c.Set(CtxIsAdmin, claims.IsAdmin)
		c.Next()
	}
}

// GinAdminOnly 要求当前用户为管理员。需在 GinJWTAuth 之后使用。
func GinAdminOnly() gin.HandlerFunc {
	return func(c *gin.Context) {
		isAdmin, _ := c.Get(CtxIsAdmin)
		if !isAdmin.(bool) {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"code":    string(domain.CodeForbidden),
				"message": "需要管理员权限",
			})
			return
		}
		c.Next()
	}
}

func abortUnauthorized(c *gin.Context, msg string) {
	c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
		"code":    string(domain.CodeUnauthorized),
		"message": msg,
	})
}
