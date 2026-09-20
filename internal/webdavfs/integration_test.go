//go:build integration

// webdavfs 写挂载集成测试：连真实 PostgreSQL + 内存假对象存储，验证 W2
// 写链路的 SQL 语义——PUT 元数据事务（哈希/引用计数/存储配额/outbox）、
// 秒传复用、覆盖旧版进回收站、MOVE 循环引用。纯函数单测覆盖不了的
// 数据库行为都在这里。
//
// 运行前置（见 deploy/README.md）：
//	cd deploy/infra && docker compose up -d
// 环境变量（未设置则跳过）：
//	NIMBUS_TEST_DSN=host=127.0.0.1 port=5432 user=nimbus password=nimbus dbname=nimbusdrive sslmode=disable
// 运行：go test -tags integration ./internal/webdavfs/

package webdavfs

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"testing"
	"time"

	"github.com/Demetrius2107/NimbusDrive/internal/domain"
	"github.com/Demetrius2107/NimbusDrive/internal/store"
	"github.com/minio/minio-go/v7"
)

var davCtx = context.Background()

var davSeq = time.Now().UnixNano()

// newDavFS 连接测试库并构造可写 FS（对象存储用内存假实现）；
// NIMBUS_TEST_DSN 未设置时跳过整个测试。
func newDavFS(t *testing.T) (*FS, *store.Store) {
	t.Helper()
	dsn := os.Getenv("NIMBUS_TEST_DSN")
	if dsn == "" {
		t.Skip("NIMBUS_TEST_DSN 未设置，跳过集成测试")
	}
	st, err := store.New(davCtx, dsn, 5, 2)
	if err != nil {
		t.Fatalf("connect test postgres: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	fake := &fakeBlob{
		objects:  map[string][]byte{},
		sessions: map[string]string{},
		parts:    map[string][][]byte{},
	}
	return New(st.DB, st.Repos(), fake, store.NewOutboxRepo(st.DB)), st
}

// davUser 创建测试用户（1MiB 配额便于通过常规写入），级联清理。
func davUser(t *testing.T, st *store.Store) int64 {
	t.Helper()
	suffix := fmt.Sprintf("%d_%d", davSeq, time.Now().UnixNano())
	u, err := st.Repos().Users.Create(davCtx,
		"wdav_"+suffix, "wdav_"+suffix+"@test.local", "wdav-hash")
	if err != nil {
		t.Fatalf("create test user: %v", err)
	}
	if _, err := st.DB.Exec("UPDATE users SET storage_quota = 1048576 WHERE id = $1", u.ID); err != nil {
		t.Fatalf("raise test quota: %v", err)
	}
	t.Cleanup(func() {
		_, _ = st.DB.Exec("DELETE FROM users WHERE id = $1", u.ID)
	})
	return u.ID
}

// fakeSeekCloser 补上 bytes.Reader 缺失的 Close，凑齐 io.ReadSeekCloser。
type fakeSeekCloser struct{ *bytes.Reader }

func (fakeSeekCloser) Close() error { return nil }

// fakeBlob 是 BlobStore 的内存假实现：集成测试不连 MinIO，只保证消费方
// 需要的语义——内容寻址 key 下存字节、multipart 会话合并尾块。
type fakeBlob struct {
	objects  map[string][]byte
	sessions map[string]string // uploadID -> objectKey
	parts    map[string][][]byte
}

func (f *fakeBlob) GetObjectStream(_ context.Context, objectKey string, _ minio.GetObjectOptions) (io.ReadSeekCloser, error) {
	b, ok := f.objects[objectKey]
	if !ok {
		return nil, fmt.Errorf("fake blob: object %q 不存在", objectKey)
	}
	return fakeSeekCloser{bytes.NewReader(b)}, nil
}

func (f *fakeBlob) PutObject(_ context.Context, objectKey string, reader io.Reader, _ int64) error {
	b, err := io.ReadAll(reader)
	if err != nil {
		return err
	}
	f.objects[objectKey] = b
	return nil
}

func (f *fakeBlob) CopyObject(_ context.Context, srcKey, dstKey string) error {
	src, ok := f.objects[srcKey]
	if !ok {
		return fmt.Errorf("fake blob: 源对象 %q 不存在", srcKey)
	}
	dst := make([]byte, len(src))
	copy(dst, src)
	f.objects[dstKey] = dst
	return nil
}

func (f *fakeBlob) RemoveObject(_ context.Context, objectKey string) error {
	delete(f.objects, objectKey)
	return nil
}

func (f *fakeBlob) CreateMultipartUpload(_ context.Context, objectKey string) (string, error) {
	id := fmt.Sprintf("sess-%d", time.Now().UnixNano())
	f.sessions[id] = objectKey
	return id, nil
}

func (f *fakeBlob) UploadPart(_ context.Context, _, uploadID string, partNumber int, reader io.Reader, _ int64) (string, error) {
	b, err := io.ReadAll(reader)
	if err != nil {
		return "", err
	}
	f.parts[uploadID] = append(f.parts[uploadID], b)
	return fmt.Sprintf("etag-%s-%d", uploadID, partNumber), nil
}

func (f *fakeBlob) CompleteMultipartUpload(_ context.Context, objectKey, uploadID string, _ []minio.CompletePart) error {
	parts, ok := f.parts[uploadID]
	if !ok {
		return fmt.Errorf("fake blob: 会话 %q 不存在", uploadID)
	}
	var merged []byte
	for _, p := range parts {
		merged = append(merged, p...)
	}
	f.objects[objectKey] = merged
	delete(f.parts, uploadID)
	delete(f.sessions, uploadID)
	return nil
}

func (f *fakeBlob) AbortMultipartUpload(_ context.Context, _, uploadID string) error {
	delete(f.parts, uploadID)
	delete(f.sessions, uploadID)
	return nil
}

// putContent 用 webdav.Handler 同款标志位写入一段内容（各用例复用）。
func put(t *testing.T, fs *FS, ctx context.Context, name string, content []byte) {
	t.Helper()
	fw, err := fs.OpenFile(ctx, name, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		t.Fatalf("open %s: %v", name, err)
	}
	if _, err := fw.Write(content); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	if err := fw.Close(); err != nil {
		t.Fatalf("close %s: %v", name, err)
	}
}

// TestDavDeepPathResolve：深路径解析（逐段 GetChildByName 的多段分支）。
func TestDavDeepPathResolve(t *testing.T) {
	fs, st := newDavFS(t)
	uid := davUser(t, st)
	ctx := WithUserID(davCtx, uid)

	if err := fs.Mkdir(ctx, "/wdav目录", 0o755); err != nil {
		t.Fatalf("mkdir root: %v", err)
	}
	// 同名冲突（唯一索引兜底）→ os.ErrExist
	if err := fs.Mkdir(ctx, "/wdav目录", 0o755); !errors.Is(err, os.ErrExist) {
		t.Fatalf("duplicate mkdir err = %v, want ErrExist", err)
	}
	if err := fs.Mkdir(ctx, "/wdav目录/子目录", 0o755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}
	info, err := fs.Stat(ctx, "/wdav目录/子目录")
	if err != nil || !info.IsDir() {
		t.Fatalf("stat nested = (%v, %v), want dir", info, err)
	}
	// 父目录缺失 → os.ErrNotExist（webdav.Handler 映射 409）
	if err := fs.Mkdir(ctx, "/no/such/parent", 0o755); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing parent err = %v, want ErrNotExist", err)
	}
	content := []byte("deep-path-content")
	put(t, fs, ctx, "/wdav目录/子目录/文件.txt", content)
	fi, err := fs.Stat(ctx, "/wdav目录/子目录/文件.txt")
	if err != nil || fi.IsDir() || fi.Size() != int64(len(content)) {
		t.Fatalf("stat file = (%v, %v), want size %d", fi, err, len(content))
	}
}

// TestDavPutRoundTrip：PUT 往返后 files 行哈希、引用计数、存储配额一致。
func TestDavPutRoundTrip(t *testing.T) {
	fs, st := newDavFS(t)
	uid := davUser(t, st)
	ctx := WithUserID(davCtx, uid)

	content := []byte("w2-put-roundtrip")
	put(t, fs, ctx, "/roundtrip.txt", content)

	node, err := st.Repos().Files.GetChildByName(davCtx, uid, nil, "roundtrip.txt")
	if err != nil {
		t.Fatalf("get file row: %v", err)
	}
	sum := sha256.Sum256(content)
	wantSum := hex.EncodeToString(sum[:])
	if node.HashSHA256 == nil || *node.HashSHA256 != wantSum {
		t.Fatalf("files.hash = %v, want %s", node.HashSHA256, wantSum)
	}
	if node.Size != int64(len(content)) || node.Status != domain.FileStatusCompleted {
		t.Fatalf("size/status = (%d, %s), want (%d, %s)",
			node.Size, node.Status, len(content), domain.FileStatusCompleted)
	}
	hr, err := st.Repos().Hashes.Get(davCtx, wantSum)
	if err != nil || hr.RefCount != 1 {
		t.Fatalf("file_hashes ref_count = (%v, %v), want 1", hr, err)
	}
	var used int64
	if err := st.DB.Get(&used, `SELECT used_storage FROM users WHERE id = $1`, uid); err != nil {
		t.Fatalf("query used_storage: %v", err)
	}
	if used != int64(len(content)) {
		t.Fatalf("used_storage = %d, want %d", used, len(content))
	}
}

// TestDavInstantReuse：同内容不同名第二次 PUT 只加引用，不重复上传 blob。
func TestDavInstantReuse(t *testing.T) {
	fs, st := newDavFS(t)
	uid := davUser(t, st)
	ctx := WithUserID(davCtx, uid)

	content := []byte("instant-reuse-content")
	put(t, fs, ctx, "/first.txt", content)
	put(t, fs, ctx, "/second.txt", content)

	sum := sha256.Sum256(content)
	wantSum := hex.EncodeToString(sum[:])
	hr, err := st.Repos().Hashes.Get(davCtx, wantSum)
	if err != nil || hr.RefCount != 2 {
		t.Fatalf("file_hashes ref_count = (%v, %v), want 2", hr, err)
	}
	fake := fs.mc.(*fakeBlob)
	if len(fake.objects) != 1 {
		t.Fatalf("blob 应只有 1 个对象，got %d", len(fake.objects))
	}
	var fileCount int
	if err := st.DB.Get(&fileCount,
		`SELECT count(*) FROM files WHERE user_id = $1 AND name IN ('first.txt', 'second.txt')`, uid); err != nil {
		t.Fatalf("count file rows: %v", err)
	}
	if fileCount != 2 {
		t.Fatalf("files 行应 2 条，got %d", fileCount)
	}
}

// TestDavOverwriteToTrash：覆盖旧版进回收站——旧行 deleted_at 置位、
// 新行指向新 blob、回收站字节仍计入已用配额。
func TestDavOverwriteToTrash(t *testing.T) {
	fs, st := newDavFS(t)
	uid := davUser(t, st)
	ctx := WithUserID(davCtx, uid)

	first := []byte("version-one")
	second := []byte("version-two-longer")
	put(t, fs, ctx, "/over.txt", first)
	put(t, fs, ctx, "/over.txt", second)

	// 现行行是新版
	node, err := st.Repos().Files.GetChildByName(davCtx, uid, nil, "over.txt")
	if err != nil {
		t.Fatalf("get live row: %v", err)
	}
	sum2 := sha256.Sum256(second)
	wantSum2 := hex.EncodeToString(sum2[:])
	if node.HashSHA256 == nil || *node.HashSHA256 != wantSum2 {
		t.Fatalf("live row hash = %v, want %s", node.HashSHA256, wantSum2)
	}
	// 旧行进回收站
	var trashCount int
	if err := st.DB.Get(&trashCount,
		`SELECT count(*) FROM files WHERE user_id = $1 AND deleted_at IS NOT NULL AND name = 'over.txt'`, uid); err != nil {
		t.Fatalf("count trash rows: %v", err)
	}
	if trashCount != 1 {
		t.Fatalf("回收站应有 1 条旧行，got %d", trashCount)
	}
	// used_storage = first + second 累计（删除语义 D5：回收站不递减引用）
	var used int64
	if err := st.DB.Get(&used, `SELECT used_storage FROM users WHERE id = $1`, uid); err != nil {
		t.Fatalf("query used_storage: %v", err)
	}
	if used != int64(len(first)+len(second)) {
		t.Fatalf("used_storage = %d, want %d", used, len(first)+len(second))
	}
}

// TestDavMoveIntoSubtree：目录移入自身子树 → os.ErrInvalid（MOVE 403）；
// 正常跨父移动仍可用。
func TestDavMoveIntoSubtree(t *testing.T) {
	fs, st := newDavFS(t)
	uid := davUser(t, st)
	ctx := WithUserID(davCtx, uid)

	if err := fs.Mkdir(ctx, "/m1", 0o755); err != nil {
		t.Fatalf("mkdir m1: %v", err)
	}
	if err := fs.Mkdir(ctx, "/m1/m2", 0o755); err != nil {
		t.Fatalf("mkdir m1/m2: %v", err)
	}
	if err := fs.Rename(ctx, "/m1", "/m1/m2/m1child"); !errors.Is(err, os.ErrInvalid) {
		t.Fatalf("move into subtree err = %v, want ErrInvalid", err)
	}
	if err := fs.Mkdir(ctx, "/m2root", 0o755); err != nil {
		t.Fatalf("mkdir m2root: %v", err)
	}
	if err := fs.Rename(ctx, "/m1/m2", "/m2root/m2moved"); err != nil {
		t.Fatalf("normal cross-parent move: %v", err)
	}
	if _, err := fs.Stat(ctx, "/m2root/m2moved"); err != nil {
		t.Fatalf("stat moved dir: %v", err)
	}
}

// TestDavPutQuotaExceeded：配额超限时提交事务回滚，无 files 行残留。
func TestDavPutQuotaExceeded(t *testing.T) {
	fs, st := newDavFS(t)
	uid := davUser(t, st)
	ctx := WithUserID(davCtx, uid)

	if _, err := st.DB.Exec(`UPDATE users SET storage_quota = 64 WHERE id = $1`, uid); err != nil {
		t.Fatalf("shrink quota: %v", err)
	}
	fw, err := fs.OpenFile(ctx, "/overquota.bin", os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := fw.Write(bytes.Repeat([]byte("x"), 128)); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := fw.Close(); err == nil {
		t.Fatal("超配额 close 应失败")
	}
	var n int
	if err := st.DB.Get(&n, `SELECT count(*) FROM files WHERE user_id = $1`, uid); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if n != 0 {
		t.Fatalf("失败提交不应残留 files 行，got %d", n)
	}
}
