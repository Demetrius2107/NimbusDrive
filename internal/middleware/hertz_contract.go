package middleware

import (
	"bytes"
	"context"

	"github.com/Demetrius2107/NimbusDrive/internal/contract"
	"github.com/Demetrius2107/NimbusDrive/internal/domain"
	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
)

// HertzContract 用指定 JSON Schema 契约校验请求体的 Hertz 中间件。
//
// GinContract 的 Hertz 适配版。读取 body 后用 SetBodyStream 重放，
// 保证 handler 的 BodyStream() 仍能读到完整数据。
func HertzContract(reg *contract.Registry, id contract.SchemaID) app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		body, err := c.Body()
		if err != nil {
			c.JSON(consts.StatusBadRequest, map[string]any{
				"code":    string(domain.CodeInvalidParam),
				"message": "读取请求体失败",
			})
			c.Abort()
			return
		}

		// 重放给 handler 的 BodyStream()
		c.Request.SetBodyStream(bytes.NewReader(body), len(body))

		trimmed := bytes.TrimSpace(body)
		if len(trimmed) == 0 {
			c.Next(ctx)
			return
		}

		errs, ok := reg.Validate(id, body)
		if !ok {
			c.JSON(consts.StatusBadRequest, map[string]any{
				"code":    string(domain.CodeInvalidParam),
				"message": "请求体不符合契约",
				"errors":  errs,
			})
			c.Abort()
			return
		}
		c.Next(ctx)
	}
}
