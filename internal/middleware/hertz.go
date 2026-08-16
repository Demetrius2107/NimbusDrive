// Package middleware - Hertz 适配层。
// 与 gin.go 中的逻辑对应，但因 Context 类型不同需各自实现。
package middleware

import (
	"context"
	"time"

	"github.com/Demetrius2107/NimbusDrive/internal/logger"
	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/middlewares/server/recovery"
	"github.com/cloudwego/hertz/pkg/common/utils"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// HertzRequestID 注入 X-Request-ID。
func HertzRequestID() app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		rid := string(c.Request.Header.Peek("X-Request-ID"))
		if rid == "" {
			rid = uuid.NewString()
		}
		c.Set("request_id", rid)
		c.Response.Header.Set("X-Request-ID", rid)
		c.Next(ctx)
	}
}

// HertzLogger 结构化访问日志。
func HertzLogger() app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		start := time.Now()
		c.Next(ctx)
		rid, _ := c.Get("request_id")
		logger.L.Info("http",
			zap.String("method", string(c.Request.Method())),
			zap.String("path", string(c.Request.URI().Path())),
			zap.Int("status", c.Response.StatusCode()),
			zap.Int("size", len(c.Response.Body())),
			zap.Duration("latency", time.Since(start)),
			zap.Any("request_id", rid),
			zap.String("ip", c.ClientIP()),
		)
	}
}

// HertzRecovery panic 恢复。Hertz 自带 recovery middleware，此处包装加日志。
func HertzRecovery() app.HandlerFunc {
	return recovery.Recovery(recovery.WithRecoveryHandler(func(ctx context.Context, c *app.RequestContext, err interface{}, stack []byte) {
		rid, _ := c.Get("request_id")
		logger.L.Error("panic recovered",
			zap.Any("error", err),
			zap.Any("request_id", rid),
			zap.ByteString("stack", stack),
		)
		c.AbortWithStatusJSON(consts.StatusInternalServerError, utils.H{"code": "50001", "message": "服务器内部错误"})
	}))
}
