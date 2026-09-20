package middleware

import (
	"context"
	"encoding/base64"
	"strings"
	"time"

	"github.com/Demetrius2107/NimbusDrive/internal/domain"
	"github.com/Demetrius2107/NimbusDrive/internal/logger"
	"github.com/Demetrius2107/NimbusDrive/internal/store"
	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/common/utils"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
	"go.uber.org/zap"
	"golang.org/x/crypto/bcrypt"
)

const basicRealm = `Basic realm="NimbusDrive WebDAV"`

// HertzBasicAuthAppPassword WebDAV 认证中间件（Hertz 适配）。
//
// Basic Auth 语义（设计文档 webdav.md 决策 D2）：
//   - username = 主账号用户名，password = 应用专用密码（bcrypt 比对）
//   - 传主密码一律 401：防止用户形成主密码直连习惯、把主密码播撒到系统级凭据存储
//
// 比对流程：按用户名查用户 → 取该用户全部未吊销应用密码逐个 bcrypt 比对 →
// 命中后注入 user_id/username 并懒更新 last_used_at（>1h 节流，见 AppPasswordRepo）。
func HertzBasicAuthAppPassword(users *store.UserRepo, passwords *store.AppPasswordRepo) app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		header := string(c.Request.Header.Peek("Authorization"))
		username, secret, ok := parseBasicAuth(header)
		if !ok {
			abortBasicUnauthorized(c)
			return
		}

		user, err := users.GetByUsername(ctx, username)
		if err != nil {
			// 用户不存在与密码错误统一返回 401，不泄露账号存在性
			logger.L.Debug("webdav basic auth user lookup failed", zap.String("username", username), zap.Error(err))
			abortBasicUnauthorized(c)
			return
		}
		if user.Status != 1 {
			abortBasicUnauthorized(c)
			return
		}

		list, err := passwords.ListActiveByUser(ctx, user.ID)
		if err != nil || len(list) == 0 {
			abortBasicUnauthorized(c)
			return
		}

		var matched *domain.AppPassword
		for i := range list {
			if bcrypt.CompareHashAndPassword([]byte(list[i].PasswordHash), []byte(secret)) == nil {
				matched = &list[i]
				break
			}
		}
		if matched == nil {
			abortBasicUnauthorized(c)
			return
		}

		var lastUsed *time.Time
		if matched.LastUsedAt != nil {
			if t, err := time.Parse(time.RFC3339, *matched.LastUsedAt); err == nil {
				lastUsed = &t
			}
		}
		passwords.TouchLastUsed(ctx, matched.ID, lastUsed, time.Now())

		c.Set(HertzCtxUserID, user.ID)
		c.Set(HertzCtxUsername, user.Username)
		c.Next(ctx)
	}
}

// parseBasicAuth 解析 Authorization: Basic base64(user:pass)。
func parseBasicAuth(header string) (username, password string, ok bool) {
	const prefix = "Basic "
	if len(header) < len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return "", "", false
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(header[len(prefix):]))
	if err != nil {
		return "", "", false
	}
	userPass := string(decoded)
	idx := strings.IndexByte(userPass, ':')
	if idx < 0 {
		return "", "", false
	}
	return userPass[:idx], userPass[idx+1:], true
}

func abortBasicUnauthorized(c *app.RequestContext) {
	// WWW-Authenticate 是 Basic Auth 协议要求：客户端据此弹出凭据输入
	c.Header("WWW-Authenticate", basicRealm)
	c.AbortWithStatusJSON(consts.StatusUnauthorized, utils.H{
		"code":    "40101",
		"message": "需要应用专用密码（主密码不可用于 WebDAV）",
	})
}
