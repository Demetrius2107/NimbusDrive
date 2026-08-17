// Package contract 提供 JSON Schema（draft 2020-12）契约的加载、编译与校验。
//
// 设计目标：把接口契约从散落在 struct tag 与手写校验里的隐式规则，提升为
// 机器可读、启动期编译、运行期 O(1) 查找的契约层。schema 文件经 //go:embed
// 编译期嵌入，进程启动时一次性编译；运行期只做 map 查找 + Validate。
//
// 中间件层（Gin/Hertz）调用 Registry.Validate 拦截非法请求体，handler 仅
// 负责语义校验（归属/配额/状态机），两层职责分离。
package contract

import (
	"embed"
	"fmt"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

//go:embed schemas/*.json
var schemaFS embed.FS

// SchemaID 是端点契约的稳定标识，形如 "share.create"。
// 由路由挂载处传入中间件，与 schema 文件名（去 .json）一一对应。
type SchemaID string

const (
	ShareCreate       SchemaID = "share.create"
	ShareValidate     SchemaID = "share.validate"
	UploadCheckHash   SchemaID = "upload.check-hash"
)

// ValidationError 是单条校验失败，结构化便于前端定位字段。
type ValidationError struct {
	Field   string `json:"field"`   // 实例位置（JSON Pointer 片段，如 /expire_time）
	Message string `json:"message"` // 可读错误说明
}

// Registry 持有所有已编译的 schema。启动期构建一次，运行期只读。
type Registry struct {
	schemas map[SchemaID]*jsonschema.Schema
}

// NewRegistry 从嵌入的 schema 文件构造并编译所有契约。
// 任一 schema 编译失败即返回错误——fail-fast，避免带病启动。
func NewRegistry() (*Registry, error) {
	compiler := jsonschema.NewCompiler()
	compiler.AssertFormat() // 强制 format 校验（date-time 等）生效

	entries, err := schemaFS.ReadDir("schemas")
	if err != nil {
		return nil, fmt.Errorf("读取 schemas 目录失败: %w", err)
	}

	r := &Registry{schemas: make(map[SchemaID]*jsonschema.Schema, len(entries))}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		id := SchemaID(strings.TrimSuffix(e.Name(), ".json"))

		data, err := schemaFS.ReadFile("schemas/" + e.Name())
		if err != nil {
			return nil, fmt.Errorf("读取 schema %s 失败: %w", id, err)
		}
		doc, err := jsonschema.UnmarshalJSON(strings.NewReader(string(data)))
		if err != nil {
			return nil, fmt.Errorf("解析 schema %s 失败: %w", id, err)
		}
		// 用稳定 URL 注册，避免与文件路径耦合。
		url := "https://nimbusdrive/contract/" + e.Name()
		if err := compiler.AddResource(url, doc); err != nil {
			return nil, fmt.Errorf("注册 schema %s 失败: %w", id, err)
		}
		sch, err := compiler.Compile(url)
		if err != nil {
			return nil, fmt.Errorf("编译 schema %s 失败: %w", id, err)
		}
		r.schemas[id] = sch
	}
	return r, nil
}

// Validate 用指定契约校验请求体。返回结构化错误列表与是否通过。
// body 应为已读取的原始 JSON 字节。
func (r *Registry) Validate(id SchemaID, body []byte) ([]ValidationError, bool) {
	sch, ok := r.schemas[id]
	if !ok {
		return []ValidationError{{Field: "", Message: "未知契约: " + string(id)}}, false
	}
	inst, err := jsonschema.UnmarshalJSON(strings.NewReader(string(body)))
	if err != nil {
		return []ValidationError{{Field: "", Message: "请求体不是合法 JSON: " + err.Error()}}, false
	}
	if verr, ok := sch.Validate(inst).(*jsonschema.ValidationError); ok && verr != nil {
		return flatten(verr), false
	}
	return nil, true
}

// flatten 把树状的 ValidationError 展平为字段级错误列表。
// 取叶子节点（无 Causes）的 InstanceLocation 作为 field。
func flatten(verr *jsonschema.ValidationError) []ValidationError {
	var out []ValidationError
	var walk func(*jsonschema.ValidationError)
	walk = func(e *jsonschema.ValidationError) {
		if len(e.Causes) == 0 {
			out = append(out, ValidationError{
				Field:   pointerToField(e.InstanceLocation),
				Message: e.Error(),
			})
			return
		}
		for _, c := range e.Causes {
			walk(c)
		}
	}
	walk(verr)
	return out
}

// pointerToField 把 JSON Pointer 片段转为可读字段路径。
// InstanceLocation 形如 ["", "expire_time"]，拼为 /expire_time。
func pointerToField(loc []string) string {
	if len(loc) == 0 {
		return ""
	}
	return strings.Join(loc, "/")
}
