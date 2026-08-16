package middleware

import (
	"context"

	"github.com/Demetrius2107/NimbusDrive/internal/auth"
	"github.com/Demetrius2107/NimbusDrive/internal/logger"
	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/common/utils"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
	"go.uber.org/zap"
)

// Hertz 上下文键（与 Gin 侧的常量名一致，但各自存储于 app.RequestContext）。
const (
	HertzCtxUserID   = "user_id"
	HertzCtxUsername = "username"
	HertzCtxIsAdmin  = "is_admin"
)

// HertzJWTAuth JWT 鉴权中间件（Hertz 适配）。
// 从 Authorization: Bearer <token> 解析 → 注入 user_id/username/is_admin → 失败返回 401。
// 与 APIServer 共享同一 JWT secret（同一 auth.JWTManager），但 Context 类型不同需各自实现。
func HertzJWTAuth(mgr *auth.JWTManager) app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		header := string(c.Request.Header.Peek("Authorization"))
		tokenStr, ok := auth.ExtractBearer(header)
		if !ok {
			abortHertzUnauthorized(c, "缺少认证令牌")
			return
		}
		claims, err := mgr.Parse(tokenStr)
		if err != nil {
			rid, _ := c.Get("request_id")
			logger.L.Debug("hertz jwt parse failed", zap.Error(err), zap.Any("request_id", rid))
			abortHertzUnauthorized(c, "认证令牌无效或已过期")
			return
		}
		c.Set(HertzCtxUserID, claims.UserID)
		c.Set(HertzCtxUsername, claims.Username)
		c.Set(HertzCtxIsAdmin, claims.IsAdmin)
		c.Next(ctx)
	}
}

// HertzUserID 从上下文取出当前用户 ID（handler 用）。
func HertzUserID(c *app.RequestContext) int64 {
	v, _ := c.Get(HertzCtxUserID)
	id, _ := v.(int64)
	return id
}

func abortHertzUnauthorized(c *app.RequestContext, msg string) {
	c.AbortWithStatusJSON(consts.StatusUnauthorized, utils.H{
		"code":    "40101",
		"message": msg,
	})
}
