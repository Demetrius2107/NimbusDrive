package middleware

import (
	"bytes"
	"io"
	"net/http"

	"github.com/Demetrius2107/NimbusDrive/internal/contract"
	"github.com/Demetrius2107/NimbusDrive/internal/domain"
	"github.com/gin-gonic/gin"
)

// GinContract 用指定 JSON Schema 契约校验请求体的 Gin 中间件。
//
// 职责：仅做格式校验（类型/必填/格式/枚举等），通过后 handler 自行
// ShouldBindJSON + 语义校验（归属/配额/未来时间）。两层职责分离。
//
// 空 body 兼容：当 body 为空时放行，由 handler 决定是否需要 body
// （如 share.validate 无密码分享允许空 body）。
func GinContract(reg *contract.Registry, id contract.SchemaID) gin.HandlerFunc {
	return func(c *gin.Context) {
		body, err := io.ReadAll(c.Request.Body)
		_ = c.Request.Body.Close()
		if err != nil {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{
				"code":    string(domain.CodeInvalidParam),
				"message": "读取请求体失败",
			})
			return
		}
		// 重放给 handler 的 ShouldBindJSON
		c.Request.Body = io.NopCloser(bytes.NewReader(body))

		trimmed := bytes.TrimSpace(body)
		if len(trimmed) == 0 {
			c.Next()
			return
		}

		errs, ok := reg.Validate(id, body)
		if !ok {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{
				"code":    string(domain.CodeInvalidParam),
				"message": "请求体不符合契约",
				"errors":  errs,
			})
			return
		}
		c.Next()
	}
}
