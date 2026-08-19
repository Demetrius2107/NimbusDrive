package cron

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"

	"github.com/Demetrius2107/NimbusDrive/internal/domain"
	"github.com/Demetrius2107/NimbusDrive/internal/logger"
	"github.com/jmoiron/sqlx"
)

// TestMain 初始化 logger，避免 cron tick 中 logger.L 为 nil 时 panic。
func TestMain(m *testing.M) {
	_ = logger.Init("test", "error", "")
	os.Exit(m.Run())
}

// fakeTrashPurger 记录 trash purge 的 repo 调用，用于验证 cron 触发逻辑。
type fakeTrashPurger struct {
	mu          sync.Mutex
	expired     []domain.FileNode
	descendants map[int64][]domain.FileNode
	hardDeleted []int64
	recDeleted  []int64
	failOn      int64 // 对该 ID 的 HardDelete 返回错误
}

func (f *fakeTrashPurger) ListExpiredTrash(_ context.Context, _, _ int) ([]domain.FileNode, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.expired, nil
}

func (f *fakeTrashPurger) ListDescendants(_ context.Context, id int64) ([]domain.FileNode, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.descendants[id], nil
}

func (f *fakeTrashPurger) HardDeleteRecursive(_ context.Context, id int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if id == f.failOn {
		return errors.New("simulated delete error")
	}
	f.recDeleted = append(f.recDeleted, id)
	return nil
}

func (f *fakeTrashPurger) HardDelete(_ context.Context, id int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if id == f.failOn {
		return errors.New("simulated delete error")
	}
	f.hardDeleted = append(f.hardDeleted, id)
	return nil
}

// fakeHashDecrer 记录 DecrRef 调用。
type fakeHashDecrer struct {
	mu     sync.Mutex
	decred []string
}

func (f *fakeHashDecrer) DecrRef(_ context.Context, _ sqlx.ExtContext, hash string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.decred = append(f.decred, hash)
	return nil
}

// fakeQuotaRefunder 记录 IncrUsedStorage 调用。
type fakeQuotaRefunder struct {
	mu       sync.Mutex
	refunded []struct {
		userID int64
		delta  int64
	}
}

func (f *fakeQuotaRefunder) IncrUsedStorage(_ context.Context, _ sqlx.ExtContext, userID, delta int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.refunded = append(f.refunded, struct {
		userID int64
		delta  int64
	}{userID, delta})
	return nil
}

// TestTrashPurge_tick_PurgesNodes 验证 tick 对过期节点调 HardDelete + DecrRef + 配额回补。
func TestTrashPurge_tick_PurgesNodes(t *testing.T) {
	hash := "a" + string(make([]byte, 63)) // 64-char hash
	ft := &fakeTrashPurger{
		expired: []domain.FileNode{
			{ID: 1, UserID: 10, Size: 100, IsFolder: false, HashSHA256: &hash},
		},
		descendants: map[int64][]domain.FileNode{
			1: {{ID: 1, HashSHA256: &hash}},
		},
	}
	fh := &fakeHashDecrer{}
	fu := &fakeQuotaRefunder{}
	c := &TrashPurgeCron{
		files:         ft,
		hashes:        fh,
		users:         fu,
		retentionDays: 30,
		batchSize:     10,
		interval:      3600,
	}
	c.tick(context.Background())

	if len(ft.hardDeleted) != 1 || ft.hardDeleted[0] != 1 {
		t.Fatalf("expected hard delete of node 1, got %v", ft.hardDeleted)
	}
	if len(fh.decred) != 1 || fh.decred[0] != hash {
		t.Fatalf("expected decr ref of hash, got %v", fh.decred)
	}
	if len(fu.refunded) != 1 || fu.refunded[0].userID != 10 || fu.refunded[0].delta != -100 {
		t.Fatalf("expected quota refund user=10 delta=-100, got %v", fu.refunded)
	}
}

// TestTrashPurge_tick_SkipsFailedNode 验证单个节点失败不阻塞后续。
func TestTrashPurge_tick_SkipsFailedNode(t *testing.T) {
	hash := "b" + string(make([]byte, 63))
	ft := &fakeTrashPurger{
		expired: []domain.FileNode{
			{ID: 1, UserID: 10, Size: 100, IsFolder: true},
			{ID: 2, UserID: 10, Size: 50, IsFolder: false, HashSHA256: &hash},
		},
		descendants: map[int64][]domain.FileNode{
			1: {}, 2: {{ID: 2, HashSHA256: &hash}},
		},
		failOn: 1, // 节点 1 的 HardDeleteRecursive 失败
	}
	fh := &fakeHashDecrer{}
	fu := &fakeQuotaRefunder{}
	c := &TrashPurgeCron{
		files:         ft,
		hashes:        fh,
		users:         fu,
		retentionDays: 30,
		batchSize:     10,
		interval:      3600,
	}
	c.tick(context.Background())

	// 节点 1 失败，节点 2 应正常删除
	if len(ft.hardDeleted) != 1 || ft.hardDeleted[0] != 2 {
		t.Fatalf("expected node 2 hard-deleted after node 1 failure, got %v", ft.hardDeleted)
	}
}
