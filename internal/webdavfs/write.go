package webdavfs

// 写挂载实现（W2）：PUT（D4 全流程）+ MKCOL + DELETE + MOVE。
// webdav.Handler 把 PROPFIND XML / Depth / Destination / LOCK 等协议细节
// 处理完后回调本文件的 FileSystem 方法；错误选择遵循它源码里的状态码映射：
//
//	os.ErrNotExist → 409（PUT/MKCOL 父目录缺失）、404（DELETE 目标不存在）
//	os.ErrExist    → 405（MKCOL 集合已存在）
//	os.ErrInvalid  → 403（MOVE 目录移入自身子树）
//
// 详见 docs/protocol-specs/webdav.md。

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"mime"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Demetrius2107/NimbusDrive/internal/domain"
	"github.com/Demetrius2107/NimbusDrive/internal/storage"
	"github.com/Demetrius2107/NimbusDrive/internal/tracing"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/minio/minio-go/v7"
	"golang.org/x/net/webdav"
)

// writePartSize 是 PUT 写入路径的 Part 累积阈值：攒满 16MiB 才初始化 multipart
// 并冲刷一个 Part，未满则留在内存缓冲，Close 时一次性直传。
// S3 multipart 规定非末块 ≥5MiB，16MB 折中内存占用与 Part 数量。
const writePartSize = 16 << 20

// writeTmpPrefix 是 PUT 临时对象的前缀：tmp/webdav/{uuid}。
// 合并完成后服务端拷贝到内容寻址 key，临时对象随即删除。
const writeTmpPrefix = "tmp/webdav/"

// BlobStore 是写挂载需要的对象存储能力。消费方接口模式：
// 接口定义在使用方 webdavfs，由 *storage.MinIO 实现，集成测试用内存假实现。
type BlobStore interface {
	GetObjectStream(ctx context.Context, objectKey string, opts minio.GetObjectOptions) (io.ReadSeekCloser, error)
	PutObject(ctx context.Context, objectKey string, reader io.Reader, size int64) error
	CopyObject(ctx context.Context, srcKey, dstKey string) error
	RemoveObject(ctx context.Context, objectKey string) error
	CreateMultipartUpload(ctx context.Context, objectKey string) (string, error)
	UploadPart(ctx context.Context, objectKey, uploadID string, partNumber int, reader io.Reader, size int64) (string, error)
	CompleteMultipartUpload(ctx context.Context, objectKey, uploadID string, parts []minio.CompletePart) error
	AbortMultipartUpload(ctx context.Context, objectKey, uploadID string) error
}

// openWriteFile PUT 打开写句柄（webdav.Handler 以 O_RDWR|O_CREATE|O_TRUNC 调用，
// body 经 Write 流式写入，Close 落库）。父缺失或父是文件 → os.ErrNotExist（409）；
// 同名文件夹占用 → os.ErrExist；同名文件走覆盖（Close 事务内软删旧版）。
func (f *FS) openWriteFile(ctx context.Context, name string) (webdav.File, error) {
	uid, err := userID(ctx)
	if err != nil {
		return nil, err
	}
	parentNode, leaf, err := f.resolver.ResolveParent(ctx, uid, name)
	if err != nil {
		return nil, err
	}
	if parentNode != nil && !parentNode.IsFolder {
		// PUT /a.docx/b.txt 且 a.docx 是文件：语义上父目录不存在
		return nil, os.ErrNotExist
	}
	parentID := nodeID(parentNode)
	existing, err := f.repos.Files.GetChildByName(ctx, uid, parentID, leaf)
	if err != nil && !errors.Is(err, domain.ErrNotFound) {
		return nil, err
	}
	if existing != nil && existing.IsFolder {
		return nil, os.ErrExist
	}
	return &writeFile{
		ctx:      ctx,
		fs:       f,
		uid:      uid,
		parentID: parentID,
		name:     leaf,
		key:      writeTmpPrefix + uuid.NewString(),
		hash:     sha256.New(),
	}, nil
}

// writeFile 是 PUT 的写句柄（D4 状态机）：
// Write 阶段流式计算 sha256，攒满 writePartSize 才初始化 multipart 并冲刷
// 一个 Part（懒初始化：小文件不产生 multipart 会话）；Close 阶段按哈希分流——
// 秒传命中只引用已有对象，未命中则落 blob 再走元数据事务。
type writeFile struct {
	ctx      context.Context
	fs       *FS
	uid      int64
	parentID *int64
	name     string
	key      string
	uploadID string // 非空表示 multipart 已初始化
	parts    []minio.CompletePart
	partNum  int
	content  bytes.Buffer // 未满阈值的兜底缓冲，最多 writePartSize
	hash     hash.Hash
	size     int64
	closed   bool
}

// Write 追加写入：sha256 实时累加，攒满 writePartSize 转为 multipart Part。
func (w *writeFile) Write(p []byte) (int, error) {
	w.hash.Write(p) // sha256.Write 恒不报错
	w.size += int64(len(p))
	w.content.Write(p)
	if int64(w.content.Len()) < writePartSize {
		return len(p), nil
	}
	if err := w.ensureMultipart(); err != nil {
		return 0, err
	}
	if err := w.flushPart(); err != nil {
		return 0, err
	}
	return len(p), nil
}

// ensureMultipart 惰性初始化 multipart upload。
func (w *writeFile) ensureMultipart() error {
	if w.uploadID != "" {
		return nil
	}
	uploadID, err := w.fs.mc.CreateMultipartUpload(w.ctx, w.key)
	if err != nil {
		return fmt.Errorf("init webdav multipart: %w", err)
	}
	w.uploadID = uploadID
	return nil
}

// flushPart 把缓冲作为一个 Part 上传（S3 partNumber 从 1 开始）。
func (w *writeFile) flushPart() error {
	if w.content.Len() == 0 {
		return nil
	}
	w.partNum++
	etag, err := w.fs.mc.UploadPart(w.ctx, w.key, w.uploadID, w.partNum, &w.content, int64(w.content.Len()))
	if err != nil {
		return fmt.Errorf("upload webdav part %d: %w", w.partNum, err)
	}
	w.parts = append(w.parts, minio.CompletePart{PartNumber: w.partNum, ETag: etag})
	w.content.Reset()
	return nil
}

// Close 收尾：秒传命中只加引用；未命中先落 blob 再走元数据事务。
// 失败时中止 multipart，已上传的 Part 由对象存储按生命周期清理。
func (w *writeFile) Close() error {
	if w.closed {
		return nil
	}
	w.closed = true

	sum := hex.EncodeToString(w.hash.Sum(nil))
	contentKey := storage.ObjectKey(sum)

	// 秒传命中：同内容对象已存在，跳过合并与上传，只加引用
	hit, err := w.fs.repos.Hashes.Get(w.ctx, sum)
	if err == nil {
		w.cleanup()
		return w.commit(hit.StoragePath, sum, 0)
	}
	if !errors.Is(err, domain.ErrNotFound) {
		return err
	}

	if w.uploadID == "" {
		// 小文件：内存缓冲直接传内容寻址 key，不产生临时对象
		if err := w.fs.mc.PutObject(w.ctx, contentKey, bytes.NewReader(w.content.Bytes()), w.size); err != nil {
			return err
		}
	} else {
		// 大文件：冲刷尾块 → 合并 → 服务端拷贝到内容寻址 key → 清理临时对象
		if err := w.flushPart(); err != nil {
			w.cleanup()
			return err
		}
		if err := w.fs.mc.CompleteMultipartUpload(w.ctx, w.key, w.uploadID, w.parts); err != nil {
			w.cleanup()
			return err
		}
		w.uploadID = "" // 已合并，后续失败路径不再 abort
		if err := w.fs.mc.CopyObject(w.ctx, w.key, contentKey); err != nil {
			_ = w.fs.mc.RemoveObject(w.ctx, w.key)
			return err
		}
		_ = w.fs.mc.RemoveObject(w.ctx, w.key)
	}
	return w.commit(contentKey, sum, w.partNum)
}

// cleanup 中止未完成的 multipart。
func (w *writeFile) cleanup() {
	if w.uploadID != "" {
		_ = w.fs.mc.AbortMultipartUpload(w.ctx, w.key, w.uploadID)
	}
}

// commit 元数据事务（与 handler.UploadHandler.instantUpload 的事务模板同构）：
// 覆盖版本先在事务内软删旧行（移入回收站，删除语义 D5——回收站仍引用 blob，
// 不做引用计数递减），再引用 blob、落 completed 行、扣存储配额、写 outbox 事件。
func (w *writeFile) commit(contentKey, sum string, chunkCount int) error {
	return w.fs.withTx(w.ctx, func(tx *sqlx.Tx) error {
		// 覆盖：查找同名文件。GetChildByName 固定 r.db 不能在事务内复用，
		// 内联 SQL 与其语义对齐（completed 过滤 + parent 匹配）；
		// 只匹配文件（文件夹占用在 openWriteFile 已拒绝，竞态兜底走唯一冲突）。
		var oldID sql.NullInt64
		err := tx.GetContext(w.ctx, &oldID, `
			SELECT id FROM files
			WHERE user_id = $1 AND deleted_at IS NULL AND is_folder = false AND name = $2 AND
			      ((parent_id IS NULL AND $3::bigint IS NULL) OR parent_id = $3)`,
			w.uid, w.name, w.parentID)
		if err == nil && oldID.Valid {
			if _, err := tx.ExecContext(w.ctx, `UPDATE files SET deleted_at = now() WHERE id = $1`, oldID.Int64); err != nil {
				return fmt.Errorf("soft delete overwritten file: %w", err)
			}
		} else if !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("find overwritten file: %w", err)
		}

		// file_hashes upsert（ref_count++，顺带清零引用墓碑）。
		if err := w.fs.repos.Hashes.Upsert(w.ctx, tx, sum, contentKey, w.size); err != nil {
			return fmt.Errorf("upsert file_hash: %w", err)
		}
		// 新建 files 记录：不经 init 状态机直接落 completed，保留内联 INSERT
		// （同 instantUpload 先例）；唯一冲突 → 竞态下同名文件夹已占用。
		var fileID int64
		if err := tx.GetContext(w.ctx, &fileID, `
			INSERT INTO files (user_id, parent_id, name, size, mime_type, is_folder, hash_sha256, chunk_count, status, storage_path)
			VALUES ($1, $2, $3, $4, $5, false, $6, $7, 'completed', $8)
			RETURNING id`,
			w.uid, w.parentID, w.name, w.size, detectMIME(w.name), sum, chunkCount, contentKey); err != nil {
		if isUniqueViolation(err) {
			return os.ErrExist
		}
		return fmt.Errorf("insert webdav file: %w", err)
		}
		// 存储配额扣减（原子条件更新，超限回滚）。
		if err := w.fs.repos.Users.IncrUsedStorage(w.ctx, tx, w.uid, w.size); err != nil {
			return err
		}
		// outbox 同事务写入（持久化事件，由 OutboxRelay 投递）。
		if w.fs.outbox != nil {
			if _, err := w.fs.outbox.Enqueue(w.ctx, tx, domain.EventFileUploaded,
				map[string]any{
					"file_id":      fileID,
					"user_id":      w.uid,
					"hash_sha256":  sum,
					"size":         w.size,
					"storage_path": contentKey,
					"name":         w.name,
					"instant":      true,
				},
				tracing.Inject(w.ctx)); err != nil {
				return fmt.Errorf("enqueue outbox: %w", err)
			}
		}
		return nil
	})
}

// --- webdav.File 接口（写句柄只支持写，读方法返回错误） ---

func (w *writeFile) Stat() (os.FileInfo, error) {
	return putFileInfo{name: w.name, size: w.size, etag: `"` + hex.EncodeToString(w.hash.Sum(nil)) + `"`}, nil
}

func (w *writeFile) Read([]byte) (int, error) {
	return 0, errors.New("webdavfs: 对写句柄调用 Read")
}

func (w *writeFile) Seek(int64, int) (int64, error) { return 0, nil }

func (w *writeFile) Readdir(int) ([]os.FileInfo, error) {
	return nil, errors.New("webdavfs: 对写句柄调用 Readdir")
}

// putFileInfo 是写句柄 Stat 的结果：handlePut 的 findETag 从这里取强 ETag
// （sha256），不落到 mtime+size 启发式。
type putFileInfo struct {
	name string
	size int64
	etag string
}

func (fi putFileInfo) Name() string       { return fi.name }
func (fi putFileInfo) Size() int64        { return fi.size }
func (fi putFileInfo) IsDir() bool        { return false }
func (fi putFileInfo) ModTime() time.Time { return time.Now() }
func (fi putFileInfo) Mode() os.FileMode  { return 0o644 }
func (fi putFileInfo) Sys() any           { return nil }

// ETag 实现 webdav.ETager。
func (fi putFileInfo) ETag(context.Context) (string, error) { return fi.etag, nil }

// --- MKCOL / DELETE / MOVE ---

// Mkdir 创建目录（MKCOL）。父目录缺失或父是文件 → os.ErrNotExist（409）；
// 已存在 → os.ErrExist（405）。
func (f *FS) Mkdir(ctx context.Context, name string, perm os.FileMode) error {
	uid, err := userID(ctx)
	if err != nil {
		return err
	}
	parentNode, leaf, err := f.resolver.ResolveParent(ctx, uid, name)
	if err != nil {
		return err
	}
	if parentNode != nil && !parentNode.IsFolder {
		return os.ErrNotExist
	}
	parentID := nodeID(parentNode)
	existing, err := f.repos.Files.GetChildByName(ctx, uid, parentID, leaf)
	if err != nil && !errors.Is(err, domain.ErrNotFound) {
		return err
	}
	if existing != nil {
		return os.ErrExist
	}
	_, err = f.repos.Files.Create(ctx, &domain.FileNode{
		UserID:   uid,
		ParentID: parentID,
		Name:     leaf,
		IsFolder: true,
		Status:   domain.FileStatusCompleted,
		MimeType: "inode/directory",
	})
	if errors.Is(err, domain.ErrConflict) {
		return os.ErrExist
	}
	return err
}

// RemoveAll 删除（DELETE）：只软删进回收站（D5 语义，WebDAV 无硬删路径）。
// 目标不存在或为根 → os.ErrNotExist（404/405）。
func (f *FS) RemoveAll(ctx context.Context, name string) error {
	uid, err := userID(ctx)
	if err != nil {
		return err
	}
	node, err := f.resolver.Resolve(ctx, uid, name)
	if err != nil {
		return err
	}
	if node == nil {
		return os.ErrNotExist
	}
	if node.IsFolder {
		return f.repos.Files.SoftDeleteRecursive(ctx, node.ID)
	}
	return f.repos.Files.SoftDelete(ctx, node.ID)
}

// Rename 移动/重命名（MOVE）。源缺失或为根 → os.ErrNotExist（403）；
// 目录移入自身子树 → os.ErrInvalid（403）。同父改名走 FileRepo.Rename；
// 跨父移动 + 改名在单条 UPDATE 内原子完成（Move/Rename 各自固定 r.db 且
// 只改单列，无法在事务内复用，内联 SQL 与二者语义对齐：deleted_at IS NULL、
// 唯一冲突 → os.ErrExist）。
func (f *FS) Rename(ctx context.Context, oldName, newName string) error {
	uid, err := userID(ctx)
	if err != nil {
		return err
	}
	src, err := f.resolver.Resolve(ctx, uid, oldName)
	if err != nil {
		return err
	}
	if src == nil {
		return os.ErrNotExist
	}
	dstParent, dstLeaf, err := f.resolver.ResolveParent(ctx, uid, newName)
	if err != nil {
		return err
	}
	if dstParent != nil && !dstParent.IsFolder {
		return os.ErrNotExist
	}
	dstParentID := nodeID(dstParent)
	if src.IsFolder && dstParent != nil {
		// 目录不能移入自身子树：目标父出现在源子树内即循环引用
		sub, err := f.repos.Files.Subtree(ctx, src.ID)
		if err != nil {
			return err
		}
		for i := range sub {
			if sub[i].ID == dstParent.ID {
				return os.ErrInvalid
			}
		}
	}
	if sameParent(src.ParentID, dstParentID) {
		if src.Name == dstLeaf {
			return nil
		}
		return f.repos.Files.Rename(ctx, src.ID, dstLeaf)
	}
	_, err = f.db.ExecContext(ctx,
		`UPDATE files SET parent_id = $2, name = $3 WHERE id = $1 AND deleted_at IS NULL`, src.ID, dstParentID, dstLeaf)
	if err != nil {
		if isUniqueViolation(err) {
			return os.ErrExist
		}
		return fmt.Errorf("move webdav node: %w", err)
	}
	return nil
}

// sameParent 判断两个父目录引用是否指向同一目录（nil = 用户根）。
func sameParent(a, b *int64) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// nodeID 取节点 ID 指针；nil 节点（用户根）返回 nil。
func nodeID(n *domain.FileNode) *int64 {
	if n == nil {
		return nil
	}
	return &n.ID
}

// withTx 包裹事务执行，出错自动回滚（与 handler.UploadHandler.withTx 同构；
// handler 不导出该 helper，本地复制）。
func (f *FS) withTx(ctx context.Context, fn func(*sqlx.Tx) error) error {
	tx, err := f.db.BeginTxx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

// detectMIME 按扩展名取 MIME；未知类型回退 application/octet-stream。
// handler 包的同名函数是私有映射表不可跨包导入，这里用标准库 mime 表
// （覆盖面更全）；剥掉 charset 参数保持与 mime_type 列的存量格式一致。
func detectMIME(name string) string {
	mt := mime.TypeByExtension(filepath.Ext(strings.ToLower(name)))
	if mt == "" {
		return "application/octet-stream"
	}
	if i := strings.Index(mt, ";"); i >= 0 {
		mt = strings.TrimSpace(mt[:i])
	}
	return mt
}

// isUniqueViolation 与 store 包的同名私有函数同语义（唯一约束冲突字符串匹配）；
// store 不导出该 helper，本地复制 5 行。
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "unique constraint") || strings.Contains(msg, "duplicate key")
}

// 确认 webdav.File 接口在编译期被完整实现。
var (
	_ webdav.File   = (*writeFile)(nil)
	_ os.FileInfo   = putFileInfo{}
	_ webdav.ETager = putFileInfo{}
)
