// Package response 提供统一 JSON 响应结构，供 APIServer(Gin) 与 TransferServer(Hertz) 共用。
// 两框架的 Context 类型不同，故此处只定义纯数据结构与序列化辅助，
// 各服务在 handler 内用各自框架的方式写入 HTTP 响应。
package response

import (
	"net/http"

	"github.com/Demetrius2107/NimbusDrive/internal/domain"
)

// Body 是统一响应体。
type Body struct {
	Code    domain.ErrorCode `json:"code"`
	Message string           `json:"message"`
	Data    any              `json:"data,omitempty"`
}

// OK 构造成功响应。
func OK(data any) Body {
	return Body{Code: domain.CodeOK, Message: "ok", Data: data}
}

// FromError 从 error 构造响应体与 HTTP 状态码。
func FromError(err error) (Body, int) {
	be := domain.AsBizError(err)
	return Body{Code: be.Code, Message: be.Message}, be.HTTP
}

// HTTPStatus 从错误推导 HTTP 状态码（不携带 body，供框架适配层使用）。
func HTTPStatus(err error) int {
	be := domain.AsBizError(err)
	if be.HTTP == 0 {
		return http.StatusInternalServerError
	}
	return be.HTTP
}
