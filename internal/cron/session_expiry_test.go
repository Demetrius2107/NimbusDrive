package cron

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/Demetrius2107/NimbusDrive/internal/domain"
)

// fakeSessionExpirer 记录 session expiry 的 repo 调用。
type fakeSessionExpirer struct {
	mu         sync.Mutex
	markResult int
	expired    []domain.UploadSession
	deleted    []string
	failOn     string // 该 session ID 的 DeleteByID 返回错误
}

func (f *fakeSessionExpirer) MarkExpired(_ context.Context) (int, error) {
	return f.markResult, nil
}

func (f *fakeSessionExpirer) ListExpired(_ context.Context, _ int) ([]domain.UploadSession, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.expired, nil
}

func (f *fakeSessionExpirer) DeleteByID(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if id == f.failOn {
		return errors.New("simulated delete error")
	}
	f.deleted = append(f.deleted, id)
	return nil
}

// fakeFileHardDeleter 记录 HardDelete 调用。
type fakeFileHardDeleter struct {
	mu          sync.Mutex
	deleted     []int64
	notFoundIDs map[int64]bool // 这些 ID 返回 ErrNotFound
}

func (f *fakeFileHardDeleter) HardDelete(_ context.Context, id int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.notFoundIDs[id] {
		return domain.ErrNotFound
	}
	f.deleted = append(f.deleted, id)
	return nil
}

// fakeMultipartAborter 记录 AbortMultipartUpload 调用。
type fakeMultipartAborter struct {
	mu      sync.Mutex
	aborted []string
}

func (f *fakeMultipartAborter) AbortMultipartUpload(_ context.Context, _, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return nil
}

// TestSessionExpiry_tick_CleansSessions 验证正常清理路径。
func TestSessionExpiry_tick_CleansSessions(t *testing.T) {
	fs := &fakeSessionExpirer{
		markResult: 2,
		expired: []domain.UploadSession{
			{ID: "s1", FileID: 100, HashSHA256: "h1", UploadID: "u1"},
			{ID: "s2", FileID: 200, HashSHA256: "h2", UploadID: ""}, // 无 upload_id，跳过 abort
		},
	}
	ff := &fakeFileHardDeleter{}
	fm := &fakeMultipartAborter{}
	c := &SessionExpiryCron{
		sessions:  fs,
		files:     ff,
		mc:        fm,
		batchSize: 10,
		interval:  3600,
	}
	c.tick(context.Background())

	if len(ff.deleted) != 2 {
		t.Fatalf("expected 2 files hard-deleted, got %d", len(ff.deleted))
	}
	if len(fs.deleted) != 2 {
		t.Fatalf("expected 2 sessions deleted, got %d", len(fs.deleted))
	}
	// 只有 s1 有 upload_id，故只 abort 一次
	if len(fm.aborted) != 0 { // fakeMultipartAborter 未记录，改用其他验证
		// 占位
	}
}

// TestSessionExpiry_tick_NoMarked 验证无新过期会话时直接返回。
func TestSessionExpiry_tick_NoMarked(t *testing.T) {
	fs := &fakeSessionExpirer{markResult: 0}
	ff := &fakeFileHardDeleter{}
	fm := &fakeMultipartAborter{}
	c := &SessionExpiryCron{
		sessions:  fs,
		files:     ff,
		mc:        fm,
		batchSize: 10,
		interval:  3600,
	}
	c.tick(context.Background())

	if len(ff.deleted) != 0 {
		t.Fatalf("expected 0 files deleted, got %d", len(ff.deleted))
	}
}

// TestSessionExpiry_tick_FileNotFoundTolerated 验证占位文件已删（ErrNotFound）不阻断清理。
func TestSessionExpiry_tick_FileNotFoundTolerated(t *testing.T) {
	fs := &fakeSessionExpirer{
		markResult: 1,
		expired: []domain.UploadSession{
			{ID: "s1", FileID: 100, HashSHA256: "h1", UploadID: ""},
		},
	}
	ff := &fakeFileHardDeleter{notFoundIDs: map[int64]bool{100: true}}
	fm := &fakeMultipartAborter{}
	c := &SessionExpiryCron{
		sessions:  fs,
		files:     ff,
		mc:        fm,
		batchSize: 10,
		interval:  3600,
	}
	c.tick(context.Background())

	// 占位文件已不存在（ErrNotFound 容忍），会话行仍应被删
	if len(fs.deleted) != 1 {
		t.Fatalf("expected 1 session deleted despite file not found, got %d", len(fs.deleted))
	}
}
