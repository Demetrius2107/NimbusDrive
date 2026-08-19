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
	return jwtAuth(mgr, false)
}

// GinJWTAuthAllowQuery 同 GinJWTAuth，但额外接受 ?token= query 作为令牌来源。
// 仅用于 SSE 端点：浏览器原生 EventSource 无法设置自定义请求头，
// 只能通过 query 传 JWT。普通接口仍用 GinJWTAuth（只认 header），避免 token 泄漏到访问日志。
func GinJWTAuthAllowQuery(mgr *auth.JWTManager) gin.HandlerFunc {
	return jwtAuth(mgr, true)
}

// jwtAuth 公共实现。allowQuery=true 时回退到 ?token= query。
func jwtAuth(mgr *auth.JWTManager, allowQuery bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		tokenStr, ok := auth.ExtractBearer(c.GetHeader("Authorization"))
		if !ok && allowQuery {
			if q := c.Query("token"); q != "" {
				tokenStr, ok = q, true
			}
		}
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
