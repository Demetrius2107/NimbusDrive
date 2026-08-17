package contract_test

import (
	"strings"
	"testing"

	"github.com/Demetrius2107/NimbusDrive/internal/contract"
)

// TestRegistry_LoadAll 启动期编译所有嵌入 schema 必须成功。
// 任何 schema 语法错误都会让进程带病启动，此处 fail-fast。
func TestRegistry_LoadAll(t *testing.T) {
	r, err := contract.NewRegistry()
	if err != nil {
		t.Fatalf("NewRegistry 编译失败: %v", err)
	}
	for _, id := range []contract.SchemaID{contract.ShareCreate, contract.ShareValidate, contract.UploadCheckHash} {
		if r == nil {
			t.Fatalf("registry 为 nil")
		}
		// 编译成功即可，Validate 用后续用例覆盖
		errs, ok := r.Validate(id, []byte(`{}`))
		// 空 JSON 对象不保证通过（required 的会失败），但不应 panic
		_ = errs
		_ = ok
	}
}

// TestValidate_ShareCreate 覆盖创建分享请求体的合法/非法分支。
func TestValidate_ShareCreate(t *testing.T) {
	r := mustRegistry(t)
	cases := []struct {
		name string
		body string
		want bool
	}{
		{
			"合法: 全字段",
			`{"password":"secret","expire_time":"2026-12-31T23:59:59Z","max_access_count":5}`,
			true,
		},
		{
			"合法: 仅必填省略（无 required 字段）",
			`{}`,
			true,
		},
		{
			"合法: 永不过期（省略 expire_time）",
			`{"password":"abc"}`,
			true,
		},
		{
			"非法: expire_time 非 RFC3339",
			`{"expire_time":"not-a-date"}`,
			false,
		},
		{
			"非法: password 超长",
			`{"password":"` + strings.Repeat("x", 65) + `"}`,
			false,
		},
		{
			"非法: max_access_count 非正",
			`{"max_access_count":0}`,
			false,
		},
		{
			"非法: 未知字段（additionalProperties:false）",
			`{"foo":"bar"}`,
			false,
		},
		{
			"非法: 非 JSON",
			`not json`,
			false,
		},
		{
			"非法: 非对象（数组）",
			`[]`,
			false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			errs, ok := r.Validate(contract.ShareCreate, []byte(tc.body))
			if ok != tc.want {
				t.Fatalf("Validate(%s) ok=%v want=%v errs=%+v", tc.body, ok, tc.want, errs)
			}
		})
	}
}

// TestValidate_ShareValidate 校验密码请求体。无密码分享可空 body，但契约层
// 收到的是已读出的 body，空 body 由中间件层放行，此处只测非空分支。
func TestValidate_ShareValidate(t *testing.T) {
	r := mustRegistry(t)
	cases := []struct {
		name string
		body string
		want bool
	}{
		{"合法: 带密码", `{"password":"abc"}`, true},
		{"合法: 空对象", `{}`, true},
		{"非法: password 超长", `{"password":"` + strings.Repeat("x", 65) + `"}`, false},
		{"非法: 未知字段", `{"foo":"bar"}`, false},
		{"非法: 非对象", `42`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			errs, ok := r.Validate(contract.ShareValidate, []byte(tc.body))
			if ok != tc.want {
				t.Fatalf("Validate(%s) ok=%v want=%v errs=%+v", tc.body, ok, tc.want, errs)
			}
		})
	}
}

// TestValidate_UploadCheckHash 覆盖秒传判定请求体的合法/非法分支。
func TestValidate_UploadCheckHash(t *testing.T) {
	r := mustRegistry(t)
	// 64 位小写 hex
	hash := strings.Repeat("a", 64)
	cases := []struct {
		name string
		body string
		want bool
	}{
		{
			"合法: 全字段含 parent_id",
			`{"hash_sha256":"` + hash + `","size":1024,"name":"x.txt","parent_id":3}`,
			true,
		},
		{
			"合法: 无 parent_id",
			`{"hash_sha256":"` + hash + `","size":1,"name":"x"}`,
			true,
		},
		{
			"非法: hash 非 64 位",
			`{"hash_sha256":"abc","size":1,"name":"x"}`,
			false,
		},
		{
			"非法: hash 含大写",
			`{"hash_sha256":"` + strings.Repeat("A", 64) + `","size":1,"name":"x"}`,
			false,
		},
		{
			"非法: size 非正",
			`{"hash_sha256":"` + hash + `","size":0,"name":"x"}`,
			false,
		},
		{
			"非法: name 空",
			`{"hash_sha256":"` + hash + `","size":1,"name":""}`,
			false,
		},
		{
			"非法: 缺 hash",
			`{"size":1,"name":"x"}`,
			false,
		},
		{
			"非法: 缺 size",
			`{"hash_sha256":"` + hash + `","name":"x"}`,
			false,
		},
		{
			"非法: 未知字段",
			`{"hash_sha256":"` + hash + `","size":1,"name":"x","evil":true}`,
			false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			errs, ok := r.Validate(contract.UploadCheckHash, []byte(tc.body))
			if ok != tc.want {
				t.Fatalf("Validate ok=%v want=%v errs=%+v", ok, tc.want, errs)
			}
		})
	}
}

// TestValidate_UnknownSchema 未知契约 ID 应返回失败而非 panic。
func TestValidate_UnknownSchema(t *testing.T) {
	r := mustRegistry(t)
	errs, ok := r.Validate(contract.SchemaID("nope"), []byte(`{}`))
	if ok {
		t.Fatal("未知契约应失败")
	}
	if len(errs) == 0 {
		t.Fatal("应有错误信息")
	}
}

// TestValidate_ErrorStructure 校验失败返回结构化错误，含 field 与 message。
func TestValidate_ErrorStructure(t *testing.T) {
	r := mustRegistry(t)
	errs, ok := r.Validate(contract.UploadCheckHash, []byte(`{"hash_sha256":"abc","size":1,"name":"x"}`))
	if ok {
		t.Fatal("应校验失败")
	}
	if len(errs) == 0 {
		t.Fatal("应返回错误列表")
	}
	// 至少一条错误的 field 指向 hash_sha256
	found := false
	for _, e := range errs {
		if strings.Contains(e.Field, "hash_sha256") {
			found = true
			if e.Message == "" {
				t.Fatalf("错误消息为空: %+v", e)
			}
		}
	}
	if !found {
		t.Fatalf("未找到 hash_sha256 字段错误: %+v", errs)
	}
}

func mustRegistry(t *testing.T) *contract.Registry {
	t.Helper()
	r, err := contract.NewRegistry()
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	return r
}
