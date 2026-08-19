package cron

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Demetrius2107/NimbusDrive/internal/domain"
	"github.com/Demetrius2107/NimbusDrive/internal/storage"
	"github.com/jmoiron/sqlx"
)

// fakeHashRepo 实现 fileHashGCRepo（ListZeroRef + DeleteIfZeroRef）。
type fakeHashRepo struct {
	mu          sync.Mutex
	candidates  []domain.FileHash
	deleted     []string
	reReferenced map[string]bool // 这些 hash 的 DeleteIfZeroRef 返回 false（模拟竞态）
	failDelete  string          // 该 hash 的 DeleteIfZeroRef 返回 error
}

func (f *fakeHashRepo) ListZeroRef(_ context.Context, _ sqlx.ExtContext, _ time.Time, _ int) ([]domain.FileHash, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.candidates, nil
}

func (f *fakeHashRepo) DeleteIfZeroRef(_ context.Context, _ sqlx.ExtContext, hash string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if hash == f.failDelete {
		return false, errors.New("simulated delete error")
	}
	if f.reReferenced[hash] {
		return false, nil // 竞态：行已被 re-reference
	}
	f.deleted = append(f.deleted, hash)
	return true, nil
}

// fakeObjectRemover 记录 RemoveObject 调用，可按 objectKey 模拟失败。
type fakeObjectRemover struct {
	mu        sync.Mutex
	removed   []string
	failOnKey string
}

func (f *fakeObjectRemover) RemoveObject(_ context.Context, objectKey string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if objectKey == f.failOnKey {
		return errors.New("simulated remove error")
	}
	f.removed = append(f.removed, objectKey)
	return nil
}

// TestHashGC_tick_ReclaimsObjects 验证正常 GC 路径：删对象 + 删行。
func TestHashGC_tick_ReclaimsObjects(t *testing.T) {
	h1 := "c" + string(make([]byte, 63))
	h2 := "d" + string(make([]byte, 63))
	fh := &fakeHashRepo{
		candidates: []domain.FileHash{
			{HashSHA256: h1, StoragePath: "blobs/c/d/" + h1},
			{HashSHA256: h2, StoragePath: "blobs/d/" + h2},
		},
	}
	fm := &fakeObjectRemover{}
	c := &HashGCCron{
		hashes:    fh,
		deleter:   fh,
		mc:        fm,
		grace:     1 * time.Hour,
		batchSize: 10,
		interval:  3600,
	}
	c.tick(context.Background())

	if len(fm.removed) != 2 {
		t.Fatalf("expected 2 objects removed, got %d", len(fm.removed))
	}
	if len(fh.deleted) != 2 {
		t.Fatalf("expected 2 rows deleted, got %d", len(fh.deleted))
	}
}

// TestHashGC_tick_RemoveFailureSkipsRowDelete 验证 RemoveObject 失败时不删行（下轮重试）。
func TestHashGC_tick_RemoveFailureSkipsRowDelete(t *testing.T) {
	h1 := "e" + string(make([]byte, 63))
	objKey := storage.ObjectKey(h1) // 用真实算法算 object key
	fh := &fakeHashRepo{
		candidates: []domain.FileHash{{HashSHA256: h1, StoragePath: objKey}},
	}
	fm := &fakeObjectRemover{failOnKey: objKey}
	c := &HashGCCron{
		hashes:    fh,
		deleter:   fh,
		mc:        fm,
		grace:     1 * time.Hour,
		batchSize: 10,
		interval:  3600,
	}
	c.tick(context.Background())

	if len(fh.deleted) != 0 {
		t.Fatalf("expected 0 rows deleted on remove failure, got %d", len(fh.deleted))
	}
}

// TestHashGC_tick_RaceReReferenced 验证 grace 窗口外残存竞态：对象已删但行被 re-reference。
func TestHashGC_tick_RaceReReferenced(t *testing.T) {
	h1 := "f" + string(make([]byte, 63))
	objKey := storage.ObjectKey(h1)
	fh := &fakeHashRepo{
		candidates:   []domain.FileHash{{HashSHA256: h1, StoragePath: objKey}},
		reReferenced: map[string]bool{h1: true},
	}
	fm := &fakeObjectRemover{}
	c := &HashGCCron{
		hashes:    fh,
		deleter:   fh,
		mc:        fm,
		grace:     1 * time.Hour,
		batchSize: 10,
		interval:  3600,
	}
	c.tick(context.Background())

	// 对象已删，但行未删（被 re-reference）
	if len(fm.removed) != 1 {
		t.Fatalf("expected 1 object removed, got %d", len(fm.removed))
	}
	if len(fh.deleted) != 0 {
		t.Fatalf("expected 0 rows deleted (race), got %d", len(fh.deleted))
	}
}
