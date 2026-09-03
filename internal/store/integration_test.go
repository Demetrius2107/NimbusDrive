//go:build integration

// store 层集成测试：连真实 PostgreSQL，验证 SQL 语义（位图、墓碑、配额条件更新、
// outbox 往返、事务传播）。纯函数单测覆盖不了的数据库行为都在这里。
//
// 运行前置（见 deploy/README.md）：
//	cd deploy/infra && docker compose up -d
// 环境变量（未设置则跳过）：
//	NIMBUS_TEST_DSN=host=127.0.0.1 port=5432 user=nimbus password=nimbus dbname=nimbusdrive sslmode=disable
// 运行：go test -tags integration ./internal/store/
package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/Demetrius2107/NimbusDrive/internal/domain"
)

var itestCtx = context.Background()

// newTestStore 连接测试库；NIMBUS_TEST_DSN 未设置时跳过整个测试。
func newTestStore(t *testing.T) *Store {
	t.Helper()
	dsn := os.Getenv("NIMBUS_TEST_DSN")
	if dsn == "" {
		t.Skip("NIMBUS_TEST_DSN 未设置，跳过集成测试")
	}
	st, err := New(itestCtx, dsn, 5, 2)
	if err != nil {
		t.Fatalf("connect test postgres: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

var itestSeq = time.Now().UnixNano()

// itestUser 创建测试用户（唯一用户名/邮箱，1KB 配额便于触发超限），级联清理。
func itestUser(t *testing.T, st *Store) *domain.User {
	t.Helper()
	suffix := fmt.Sprintf("%d_%d", itestSeq, time.Now().UnixNano())
	u, err := st.Repos().Users.Create(itestCtx,
		"itest_"+suffix, "itest_"+suffix+"@test.local", "itest-hash")
	if err != nil {
		t.Fatalf("create test user: %v", err)
	}
	t.Cleanup(func() {
		_, _ = st.DB.Exec("DELETE FROM users WHERE id = $1", u.ID)
	})
	return u
}

// itestFile 为用户创建一个已就绪的测试文件节点（upload_sessions 的 FK 依赖）。
func itestFile(t *testing.T, st *Store, userID int64) int64 {
	t.Helper()
	var id int64
	name := fmt.Sprintf("itest-file-%d", time.Now().UnixNano())
	if err := st.DB.Get(&id,
		`INSERT INTO files (user_id, name, size, is_folder, status, storage_path)
		 VALUES ($1, $2, 4096, false, 'ready', 'itest/' || $2) RETURNING id`,
		userID, name); err != nil {
		t.Fatalf("create test file: %v", err)
	}
	// 随用户级联删除，无需单独清理
	return id
}

// itestHash 生成 64 字符十六进制哈希（file_hashes 主键是 CHAR(64)）。
func itestHash(seed string) string {
	sum := sha256.Sum256([]byte(seed + fmt.Sprint(time.Now().UnixNano())))
	return hex.EncodeToString(sum[:])
}

// zeroRefContains 判断 ListZeroRef(before) 是否包含指定哈希。
func zeroRefContains(t *testing.T, st *Store, hash string, before time.Time) bool {
	t.Helper()
	list, err := st.Repos().Hashes.ListZeroRef(itestCtx, st.DB, before, 1000)
	if err != nil {
		t.Fatalf("list zero-ref: %v", err)
	}
	for _, h := range list {
		if h.HashSHA256 == hash {
			return true
		}
	}
	return false
}

// TestFileHashRefLifecycle 验证引用计数全生命周期：Upsert 累加、DecrRef 墓碑化
// （归零不删行）、re-Upsert 清墓碑、DeleteIfZeroRef 再校验删除。
// 对应迁移 0004 的 tombstone + grace 设计与 GC 删除序。
func TestFileHashRefLifecycle(t *testing.T) {
	st := newTestStore(t)
	hashes := st.Repos().Hashes
	hash := itestHash("lifecycle")
	cleanup := func() { _, _ = st.DB.Exec("DELETE FROM file_hashes WHERE hash_sha256 = $1", hash) }
	cleanup()
	t.Cleanup(cleanup)

	// 首次 Upsert：ref=1
	if err := hashes.Upsert(itestCtx, st.DB, hash, "itest/path1", 100); err != nil {
		t.Fatalf("upsert 1: %v", err)
	}
	h, err := hashes.Get(itestCtx, hash)
	if err != nil || h.RefCount != 1 {
		t.Fatalf("after upsert 1: ref=%v err=%v", h, err)
	}

	// 并发重传同哈希：ref=2
	if err := hashes.Upsert(itestCtx, st.DB, hash, "itest/path1", 100); err != nil {
		t.Fatalf("upsert 2: %v", err)
	}

	// 删一个文件：ref=1，无墓碑
	if err := hashes.DecrRef(itestCtx, st.DB, hash); err != nil {
		t.Fatalf("decr 1: %v", err)
	}
	if h, _ = hashes.Get(itestCtx, hash); h.RefCount != 1 {
		t.Fatalf("after decr 1: ref=%d, want 1", h.RefCount)
	}

	// 删最后一个文件：ref=0 + 墓碑，行保留（GC 候选）
	if err := hashes.DecrRef(itestCtx, st.DB, hash); err != nil {
		t.Fatalf("decr 2: %v", err)
	}
	if h, _ = hashes.Get(itestCtx, hash); h == nil {
		t.Fatal("zero-ref row must be kept (tombstone), got deleted")
	}
	// 墓碑新鲜：grace 窗口前（早于 now-24h）不可见；窗口后可见
	if zeroRefContains(t, st, hash, time.Now().Add(-24*time.Hour)) {
		t.Fatal("fresh tombstone must not be GC-eligible before grace expires")
	}
	if !zeroRefContains(t, st, hash, time.Now().Add(time.Second)) {
		t.Fatal("tombstone row should appear in ListZeroRef after grace window")
	}

	// grace 窗口内 re-reference：清墓碑 + ref=1，移出 GC 候选
	if err := hashes.Upsert(itestCtx, st.DB, hash, "itest/path2", 200); err != nil {
		t.Fatalf("re-upsert: %v", err)
	}
	if h, _ = hashes.Get(itestCtx, hash); h.RefCount != 1 || h.StoragePath != "itest/path2" {
		t.Fatalf("after re-upsert: %+v", h)
	}
	if zeroRefContains(t, st, hash, time.Now().Add(time.Second)) {
		t.Fatal("re-referenced row must be excluded from GC candidates")
	}

	// 再归零后 DeleteIfZeroRef：GC 删对象后的行清理
	if err := hashes.DecrRef(itestCtx, st.DB, hash); err != nil {
		t.Fatalf("decr 3: %v", err)
	}
	deleted, err := hashes.DeleteIfZeroRef(itestCtx, st.DB, hash)
	if err != nil || !deleted {
		t.Fatalf("delete-if-zero: deleted=%v err=%v", deleted, err)
	}
	if _, err := hashes.Get(itestCtx, hash); err != domain.ErrNotFound {
		t.Fatalf("row should be gone, got err=%v", err)
	}
	// 再删：affected=0（已被 re-reference 时调用方据此跳过）
	if deleted, _ := hashes.DeleteIfZeroRef(itestCtx, st.DB, hash); deleted {
		t.Fatal("second delete should affect 0 rows")
	}
}

// TestFileHashTxRollback 验证 executor 接口模式：Upsert 接受 Tx，
// 事务回滚后写入不留痕迹（completeTransaction 事务性的基础）。
func TestFileHashTxRollback(t *testing.T) {
	st := newTestStore(t)
	hash := itestHash("rollback")
	cleanup := func() { _, _ = st.DB.Exec("DELETE FROM file_hashes WHERE hash_sha256 = $1", hash) }
	cleanup()
	t.Cleanup(cleanup)

	tx, err := st.DB.BeginTxx(itestCtx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Repos().Hashes.Upsert(itestCtx, tx, hash, "itest/tx", 1); err != nil {
		t.Fatalf("upsert in tx: %v", err)
	}
	// 事务内可见
	var count int
	if err := tx.Get(&count, `SELECT count(*) FROM file_hashes WHERE hash_sha256 = $1`, hash); err != nil || count != 1 {
		t.Fatalf("in-tx visibility: count=%d err=%v", count, err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Repos().Hashes.Get(itestCtx, hash); err != domain.ErrNotFound {
		t.Fatalf("rolled-back upsert must leave no row, got err=%v", err)
	}
}

// TestUploadSessionBitmap 验证分块位图的真实 DB 语义：乱序置位、重复置位幂等、
// 位图扩容、MissingChunks 补集、Complete 状态机。
func TestUploadSessionBitmap(t *testing.T) {
	st := newTestStore(t)
	repos := st.Repos()
	u := itestUser(t, st)
	fileID := itestFile(t, st, u.ID)

	const total = 12
	sid, err := repos.Uploads.Create(itestCtx, &domain.UploadSession{
		UserID: u.ID, FileID: fileID, HashSHA256: itestHash("session"),
		TotalSize: total * 4 * 1024 * 1024, ChunkSize: 4 * 1024 * 1024,
		TotalChunks: total, UploadID: "itest-multipart-id",
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	// 会话随测试用户级联删除（FK ON DELETE CASCADE），无需单独清理

	// 乱序 + 重复置位
	for _, idx := range []int{0, 11, 5, 5, 0} {
		if err := repos.Uploads.MarkChunkUploaded(itestCtx, sid, idx, total); err != nil {
			t.Fatalf("mark chunk %d: %v", idx, err)
		}
	}
	s, err := repos.Uploads.Get(itestCtx, sid)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.UploadedChunks) != 2 { // ceil(12/8)=2 字节，扩容正确
		t.Fatalf("bitmap len=%d, want 2", len(s.UploadedChunks))
	}
	missing, err := repos.Uploads.MissingChunks(itestCtx, sid)
	if err != nil {
		t.Fatal(err)
	}
	if len(missing) != total-3 { // 0、5、11 已置位（重复置位幂等）
		t.Fatalf("missing=%v, want %d chunks", missing, total-3)
	}

	// 补齐 → MissingChunks 为空 → Complete
	for _, idx := range missing {
		if err := repos.Uploads.MarkChunkUploaded(itestCtx, sid, idx, total); err != nil {
			t.Fatalf("mark chunk %d: %v", idx, err)
		}
	}
	if missing, _ = repos.Uploads.MissingChunks(itestCtx, sid); len(missing) != 0 {
		t.Fatalf("missing after fill: %v", missing)
	}
	if err := repos.Uploads.Complete(itestCtx, sid); err != nil {
		t.Fatalf("complete: %v", err)
	}
	if s, _ = repos.Uploads.Get(itestCtx, sid); s.Status != domain.UploadSessionComplete {
		t.Fatalf("status=%s, want completed", s.Status)
	}
	// 已完成的会话不可再 Complete（affected=0 → ErrNotFound）
	if err := repos.Uploads.Complete(itestCtx, sid); err != domain.ErrNotFound {
		t.Fatalf("second complete should be ErrNotFound, got %v", err)
	}
}

// TestUserUsedStorageQuota 验证存储配额原子条件更新：累计、超限拒绝、
// 回补、不为负（GREATEST 语义由 WHERE used_storage+delta>=0 保证）。
func TestUserUsedStorageQuota(t *testing.T) {
	st := newTestStore(t)
	repos := st.Repos()
	u := itestUser(t, st)
	if err := repos.Users.SetQuota(itestCtx, u.ID, 1000); err != nil {
		t.Fatalf("set quota: %v", err)
	}

	if err := repos.Users.IncrUsedStorage(itestCtx, st.DB, u.ID, 600); err != nil {
		t.Fatalf("incr +600: %v", err)
	}
	if err := repos.Users.IncrUsedStorage(itestCtx, st.DB, u.ID, 600); err != domain.ErrQuotaExceeded {
		t.Fatalf("incr +600 over quota should be ErrQuotaExceeded, got %v", err)
	}
	if err := repos.Users.IncrUsedStorage(itestCtx, st.DB, u.ID, -100); err != nil {
		t.Fatalf("incr -100: %v", err)
	}
	u2, err := repos.Users.GetByID(itestCtx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if u2.UsedStorage != 500 {
		t.Fatalf("used_storage=%d, want 500", u2.UsedStorage)
	}
	// 回补超过已用量：拒绝（不能为负）
	if err := repos.Users.IncrUsedStorage(itestCtx, st.DB, u.ID, -600); err != domain.ErrQuotaExceeded {
		t.Fatalf("incr -600 below zero should be ErrQuotaExceeded, got %v", err)
	}
}

// TestQuotaPeriodLifecycle 验证月度传输配额：GetOrCreateCurrent 幂等（同月同行）、
// IncrUpload 计量、超限 ErrQuotaExceeded、quota=0 不限。
func TestQuotaPeriodLifecycle(t *testing.T) {
	st := newTestStore(t)
	repos := st.Repos()
	u := itestUser(t, st)
	if err := repos.Users.SetQuota(itestCtx, u.ID, 1000); err != nil {
		t.Fatal(err)
	}

	qp1, err := repos.Quotas.GetOrCreateCurrent(itestCtx, u.ID)
	if err != nil {
		t.Fatalf("get-or-create: %v", err)
	}
	qp2, err := repos.Quotas.GetOrCreateCurrent(itestCtx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if qp1.ID != qp2.ID {
		t.Fatalf("same period must reuse row: %d vs %d", qp1.ID, qp2.ID)
	}
	// 派生配额来自 users.storage_quota
	if qp1.UploadQuota != 1000 {
		t.Fatalf("upload_quota=%d, want 1000 (derived)", qp1.UploadQuota)
	}

	if err := repos.Quotas.IncrUpload(itestCtx, u.ID, 400); err != nil {
		t.Fatalf("incr upload 400: %v", err)
	}
	if err := repos.Quotas.CheckUpload(itestCtx, u.ID, 700); err != domain.ErrQuotaExceeded {
		t.Fatalf("check upload 700 with 600 used should exceed, got %v", err)
	}
	if err := repos.Quotas.IncrUpload(itestCtx, u.ID, 700); err != domain.ErrQuotaExceeded {
		t.Fatalf("incr upload 700 over limit should exceed, got %v", err)
	}
	if err := repos.Quotas.IncrDownload(itestCtx, u.ID, 999); err != nil {
		t.Fatalf("incr download 999: %v", err)
	}

	// quota=0 表示不限
	if _, err := st.DB.Exec(`UPDATE quota_periods SET upload_quota = 0 WHERE id = $1`, qp1.ID); err != nil {
		t.Fatal(err)
	}
	if err := repos.Quotas.IncrUpload(itestCtx, u.ID, 1<<40); err != nil {
		t.Fatalf("unlimited quota should accept any incr, got %v", err)
	}
}

// TestOutboxRoundTrip 验证事务型 outbox 往返：Enqueue → FetchPending（SKIP LOCKED
// 需事务）→ MarkPublished 后不再出现；IncAttempt 计数。
func TestOutboxRoundTrip(t *testing.T) {
	st := newTestStore(t)
	outbox := NewOutboxRepo(st.DB)

	id, err := outbox.Enqueue(itestCtx, st.DB, "file.uploaded",
		map[string]any{"file_id": 123, "size": 456},
		map[string]string{"traceparent": "00-abc-def-01"})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	t.Cleanup(func() { _, _ = st.DB.Exec(`DELETE FROM outbox WHERE id = $1`, id) })

	// FetchPending 必须在事务内（FOR UPDATE）
	tx, err := st.DB.BeginTxx(itestCtx, nil)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := outbox.FetchPending(itestCtx, tx, 100)
	if err != nil {
		t.Fatal(err)
	}
	var found *OutboxMessage
	for i := range pending {
		if pending[i].ID == id {
			found = &pending[i]
		}
	}
	if found == nil {
		t.Fatal("enqueued message not in pending list")
	}
	if found.EventType != "file.uploaded" || found.Attempt != 0 {
		t.Fatalf("unexpected message: %+v", found)
	}
	if err := outbox.IncAttempt(itestCtx, tx, id); err != nil {
		t.Fatalf("inc attempt: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	if err := outbox.MarkPublished(itestCtx, st.DB, id); err != nil {
		t.Fatalf("mark published: %v", err)
	}
	tx2, err := st.DB.BeginTxx(itestCtx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx2.Rollback() }()
	pending2, err := outbox.FetchPending(itestCtx, tx2, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range pending2 {
		if m.ID == id {
			t.Fatal("published message must not be fetched again")
		}
	}
	if n, _ := outbox.CountPending(itestCtx); n < 0 {
		t.Fatal("count pending failed")
	}
}
