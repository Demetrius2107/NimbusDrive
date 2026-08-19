package handler

import (
	"context"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"github.com/Demetrius2107/NimbusDrive/internal/cache"
	"github.com/Demetrius2107/NimbusDrive/internal/domain"
	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/common/ut"
	"github.com/cloudwego/hertz/pkg/route/param"
)

// --- mock 实现 ---

type mockTokenRedeemer struct {
	token string
	tok   cache.ShareDownloadToken
	ok    bool
	err   error
	calls int
}

func (m *mockTokenRedeemer) RedeemShareDownloadToken(ctx context.Context, token string) (cache.ShareDownloadToken, bool, error) {
	m.calls++
	if m.err != nil {
		return cache.ShareDownloadToken{}, false, m.err
	}
	if token != m.token {
		return cache.ShareDownloadToken{}, false, nil
	}
	return m.tok, m.ok, nil
}

type mockFileGetter struct {
	file *domain.FileNode
	err  error
}

func (m *mockFileGetter) GetByID(ctx context.Context, id int64) (*domain.FileNode, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.file, nil
}

type mockPresigner struct {
	url string
	err error
}

func (m *mockPresigner) PresignedDownloadURL(ctx context.Context, storagePath string, expire int, filename, mimeType string) (string, error) {
	if m.err != nil {
		return "", m.err
	}
	return m.url, nil
}

// newRedeemCtx 构造一个带 :token 路径参数的 Hertz RequestContext。
func newRedeemCtx(token string) *app.RequestContext {
	c := ut.CreateUtRequestContext("GET", "/api/v1/s/download/"+token, nil)
	c.Params = param.Params{{Key: "token", Value: token}}
	return c
}

// validHexToken 生成一个合法的 64 字符 hex 令牌。
func validHexToken() string {
	b := make([]byte, 32)
	for i := range b {
		b[i] = 0xAB
	}
	return hex.EncodeToString(b)
}

// --- validDownloadToken ---

func TestValidDownloadToken(t *testing.T) {
	tok := validHexToken()
	if !validDownloadToken(tok) {
		t.Error("合法 64 hex 令牌应通过校验")
	}
	// 长度不对
	if validDownloadToken(tok[:60]) {
		t.Error("长度不足应拒绝")
	}
	// 非 hex 字符
	bad := strings.Repeat("z", 64)
	if validDownloadToken(bad) {
		t.Error("非 hex 字符应拒绝")
	}
	// 空串
	if validDownloadToken("") {
		t.Error("空串应拒绝")
	}
}

// --- Redeem ---

// TestRedeem_InvalidTokenFormat 非法令牌格式 → 400。
func TestRedeem_InvalidTokenFormat(t *testing.T) {
	h := &ShareDownloadHandler{}
	c := newRedeemCtx("not-hex")
	h.Redeem(context.Background(), c)
	if c.Response.StatusCode() != 400 {
		t.Errorf("非法令牌格式应 400, got %d", c.Response.StatusCode())
	}
}

// TestRedeem_NilRedis 无 Redis → 503。
func TestRedeem_NilRedis(t *testing.T) {
	h := &ShareDownloadHandler{rc: nil}
	c := newRedeemCtx(validHexToken())
	h.Redeem(context.Background(), c)
	if c.Response.StatusCode() != 503 {
		t.Errorf("nil Redis 应 503, got %d", c.Response.StatusCode())
	}
}

// TestRedeem_TokenNotFound 令牌未命中/已消费 → 404。
func TestRedeem_TokenNotFound(t *testing.T) {
	r := &mockTokenRedeemer{ok: false}
	h := &ShareDownloadHandler{rc: r, presignExpireSec: 3600}
	c := newRedeemCtx(validHexToken())
	h.Redeem(context.Background(), c)
	if c.Response.StatusCode() != 404 {
		t.Errorf("未命中令牌应 404, got %d", c.Response.StatusCode())
	}
}

// TestRedeem_RedeemError 兑换出错 → 500。
func TestRedeem_RedeemError(t *testing.T) {
	r := &mockTokenRedeemer{err: errors.New("redis boom")}
	h := &ShareDownloadHandler{rc: r, presignExpireSec: 3600}
	c := newRedeemCtx(validHexToken())
	h.Redeem(context.Background(), c)
	if c.Response.StatusCode() != 500 {
		t.Errorf("兑换出错应 500, got %d", c.Response.StatusCode())
	}
}

// TestRedeem_FileNotFound 文件不存在 → 404。
func TestRedeem_FileNotFound(t *testing.T) {
	tok := validHexToken()
	r := &mockTokenRedeemer{token: tok, ok: true, tok: cache.ShareDownloadToken{FileID: 1}}
	fg := &mockFileGetter{err: domain.ErrNotFound}
	h := &ShareDownloadHandler{rc: r, files: fg, presignExpireSec: 3600}
	c := newRedeemCtx(tok)
	h.Redeem(context.Background(), c)
	if c.Response.StatusCode() != 404 {
		t.Errorf("文件不存在应 404, got %d", c.Response.StatusCode())
	}
}

// TestRedeem_FileQueryError 文件查询出错 → 500。
func TestRedeem_FileQueryError(t *testing.T) {
	tok := validHexToken()
	r := &mockTokenRedeemer{token: tok, ok: true, tok: cache.ShareDownloadToken{FileID: 1}}
	fg := &mockFileGetter{err: errors.New("db boom")}
	h := &ShareDownloadHandler{rc: r, files: fg, presignExpireSec: 3600}
	c := newRedeemCtx(tok)
	h.Redeem(context.Background(), c)
	if c.Response.StatusCode() != 500 {
		t.Errorf("文件查询出错应 500, got %d", c.Response.StatusCode())
	}
}

// TestRedeem_FileUnavailable 文件不可用（已删除/未完成/无 storage_path）→ 404。
func TestRedeem_FileUnavailable(t *testing.T) {
	tok := validHexToken()
	r := &mockTokenRedeemer{token: tok, ok: true, tok: cache.ShareDownloadToken{FileID: 1}}
	// 已软删除
	fg := &mockFileGetter{file: &domain.FileNode{DeletedAt: strPtr("2026-01-01T00:00:00Z"), Status: domain.FileStatusCompleted, StoragePath: strPtr("blobs/ab/cd/abcd")}}
	h := &ShareDownloadHandler{rc: r, files: fg, presignExpireSec: 3600}
	c := newRedeemCtx(tok)
	h.Redeem(context.Background(), c)
	if c.Response.StatusCode() != 404 {
		t.Errorf("已删除文件应 404, got %d", c.Response.StatusCode())
	}
}

// TestRedeem_PresignError 预签名失败 → 500。
func TestRedeem_PresignError(t *testing.T) {
	tok := validHexToken()
	r := &mockTokenRedeemer{token: tok, ok: true, tok: cache.ShareDownloadToken{FileID: 1}}
	fg := &mockFileGetter{file: &domain.FileNode{Status: domain.FileStatusCompleted, StoragePath: strPtr("blobs/ab/cd/abcd"), Name: "f.txt", MimeType: "text/plain"}}
	ps := &mockPresigner{err: errors.New("minio boom")}
	h := &ShareDownloadHandler{rc: r, files: fg, mc: ps, presignExpireSec: 3600}
	c := newRedeemCtx(tok)
	h.Redeem(context.Background(), c)
	if c.Response.StatusCode() != 500 {
		t.Errorf("预签名失败应 500, got %d", c.Response.StatusCode())
	}
}

// TestRedeem_Success 成功兑换 → 200 + JSON 含 url/filename/size。
func TestRedeem_Success(t *testing.T) {
	tok := validHexToken()
	r := &mockTokenRedeemer{token: tok, ok: true, tok: cache.ShareDownloadToken{FileID: 1}}
	fg := &mockFileGetter{file: &domain.FileNode{Status: domain.FileStatusCompleted, StoragePath: strPtr("blobs/ab/cd/abcd"), Name: "f.txt", MimeType: "text/plain", Size: 1024}}
	ps := &mockPresigner{url: "https://minio.example/blobs/ab/cd/abcd?signature=xxx"}
	h := &ShareDownloadHandler{rc: r, files: fg, mc: ps, presignExpireSec: 3600}
	c := newRedeemCtx(tok)
	h.Redeem(context.Background(), c)
	if c.Response.StatusCode() != 200 {
		t.Fatalf("成功应 200, got %d", c.Response.StatusCode())
	}
	body := string(c.Response.Body())
	for _, want := range []string{`"url":"https://minio.example/blobs/ab/cd/abcd?signature=xxx"`, `"filename":"f.txt"`, `"size":1024`, `"expires_in":3600`} {
		if !strings.Contains(body, want) {
			t.Errorf("响应体应含 %q, got %s", want, body)
		}
	}
}

// TestRedeem_ReplaySameToken 重放同令牌 → 第二次 404（单次消费由 Redeem mock 模拟）。
func TestRedeem_ReplaySameToken(t *testing.T) {
	tok := validHexToken()
	// mock 第一次返回 ok=true，之后返回 ok=false（模拟 Lua GET+DEL 后令牌已删）
	r := &mockTokenRedeemer{token: tok, ok: true, tok: cache.ShareDownloadToken{FileID: 1}}
	fg := &mockFileGetter{file: &domain.FileNode{Status: domain.FileStatusCompleted, StoragePath: strPtr("blobs/ab/cd/abcd"), Name: "f.txt", MimeType: "text/plain"}}
	ps := &mockPresigner{url: "https://minio.example/x"}
	h := &ShareDownloadHandler{rc: r, files: fg, mc: ps, presignExpireSec: 3600}

	// 第一次：成功
	c1 := newRedeemCtx(tok)
	h.Redeem(context.Background(), c1)
	if c1.Response.StatusCode() != 200 {
		t.Fatalf("首次兑换应 200, got %d", c1.Response.StatusCode())
	}

	// 模拟 DEL 后状态：第二次未命中
	r.ok = false
	c2 := newRedeemCtx(tok)
	h.Redeem(context.Background(), c2)
	if c2.Response.StatusCode() != 404 {
		t.Errorf("重放同令牌应 404（已消费）, got %d", c2.Response.StatusCode())
	}
}
