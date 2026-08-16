// Package middleware 提供 APIServer(Gin) 与 TransferServer(Hertz) 各自的中间件薄层。
// 因 gin.Context 与 hertz app.RequestContext 类型不互通，逻辑抽到纯函数，
// 框架适配层各自包装。详见《设计文档》3.3 节。
package middleware

import (
	"time"

	"github.com/Demetrius2107/NimbusDrive/internal/logger"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// GinRequestID 为每个请求注入 X-Request-ID。
func GinRequestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		rid := c.GetHeader("X-Request-ID")
		if rid == "" {
			rid = uuid.NewString()
		}
		c.Set("request_id", rid)
		c.Writer.Header().Set("X-Request-ID", rid)
		c.Next()
	}
}

// GinLogger 结构化访问日志。
func GinLogger() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		logger.L.Info("http",
			zap.String("method", c.Request.Method),
			zap.String("path", c.Request.URL.Path),
			zap.Int("status", c.Writer.Status()),
			zap.Int("size", c.Writer.Size()),
			zap.Duration("latency", time.Since(start)),
			zap.String("request_id", c.GetString("request_id")),
			zap.String("ip", c.ClientIP()),
		)
	}
}

// GinRecovery panic 恢复，记录堆栈并返回 500。
func GinRecovery() gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if r := recover(); r != nil {
				logger.L.Error("panic recovered",
					zap.Any("error", r),
					zap.String("request_id", c.GetString("request_id")),
					zap.Stack("stack"),
				)
				c.AbortWithStatusJSON(500, gin.H{"code": "50001", "message": "服务器内部错误"})
			}
		}()
		c.Next()
	}
}
