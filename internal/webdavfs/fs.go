package webdavfs

import (
	"context"
	"errors"
	"io"
	"os"
	"time"

	"github.com/Demetrius2107/NimbusDrive/internal/domain"
	"github.com/Demetrius2107/NimbusDrive/internal/storage"
	"github.com/Demetrius2107/NimbusDrive/internal/store"
	"github.com/jmoiron/sqlx"
	"github.com/minio/minio-go/v7"
	"golang.org/x/net/webdav"
)

// FS 是 NimbusDrive 的 webdav.FileSystem 适配器（W2 可写）。
// 官方库 webdav.Handler 负责 PROPFIND XML / Depth / LOCK 等协议细节，
// 本结构只做接口桥接：路径解析 → FileRepo，文件字节流 → 对象存储，
// 写路径（PUT/MKCOL/DELETE/MOVE）见 write.go。
type FS struct {
	db       *sqlx.DB
	repos    *store.Repositories
	mc       BlobStore
	resolver *PathResolver
	outbox   *store.OutboxRepo
}

// New 构造 FS。outbox 可为 nil（事件总线不可用时降级，跳过 outbox 写入）。
func New(db *sqlx.DB, repos *store.Repositories, mc BlobStore, outbox *store.OutboxRepo) *FS {
	return &FS{
		db:       db,
		repos:    repos,
		mc:       mc,
		resolver: NewPathResolver(repos.Files),
		outbox:   outbox,
	}
}

// Stat 解析路径并返回文件信息。根目录返回虚拟目录信息。
func (f *FS) Stat(ctx context.Context, name string) (os.FileInfo, error) {
	uid, err := userID(ctx)
	if err != nil {
		return nil, err
	}
	node, err := f.resolver.Resolve(ctx, uid, name)
	if err != nil {
		return nil, err
	}
	if node == nil {
		return rootFileInfo{}, nil
	}
	return newFileInfo(node), nil
}

// OpenFile 按打开标志分发：
// 写打开（O_WRONLY/O_RDWR/O_CREATE/O_TRUNC/O_APPEND/O_EXCL）→ openWriteFile
// （PUT 流程：Write 流式写入，Close 落库，见 write.go）；
// 只读打开：文件 → 对象存储 GetObject（原生支持 Read/Seek/Close），目录 → dirFile。
func (f *FS) OpenFile(ctx context.Context, name string, flag int, perm os.FileMode) (webdav.File, error) {
	if flag&(os.O_WRONLY|os.O_RDWR|os.O_CREATE|os.O_TRUNC|os.O_APPEND|os.O_EXCL) != 0 {
		return f.openWriteFile(ctx, name)
	}

	uid, err := userID(ctx)
	if err != nil {
		return nil, err
	}
	node, err := f.resolver.Resolve(ctx, uid, name)
	if err != nil {
		return nil, err
	}
	if node == nil {
		return &dirFile{fs: f, uid: uid, parent: nil, fi: rootFileInfo{}}, nil
	}
	fi := newFileInfo(node)
	if node.IsFolder {
		return &dirFile{fs: f, uid: uid, parent: &node.ID, fi: fi}, nil
	}

	// 非完成态/无存储路径的文件已在 GetChildByName 过滤，此处兜底
	if node.StoragePath == nil {
		return nil, os.ErrNotExist
	}
	obj, err := f.mc.GetObjectStream(ctx, *node.StoragePath, minio.GetObjectOptions{})
	if err != nil {
		return nil, err
	}
	return &readFile{obj: obj, fi: fi}, nil
}

// --- FileInfo ---

// fileInfo 把 domain.FileNode 适配为 os.FileInfo，并实现
// webdav.ETager（ETag = sha256，内容寻址存储天然强 ETag）与
// webdav.ContentTyper（MIME 直接取 mime_type 列）。
type fileInfo struct {
	node *domain.FileNode
}

func newFileInfo(node *domain.FileNode) fileInfo { return fileInfo{node: node} }

func (fi fileInfo) Name() string { return fi.node.Name }
func (fi fileInfo) Size() int64  { return fi.node.Size }
func (fi fileInfo) IsDir() bool  { return fi.node.IsFolder }
func (fi fileInfo) ModTime() time.Time {
	return parseDBTime(fi.node.UpdatedAt)
}
func (fi fileInfo) Mode() os.FileMode {
	if fi.node.IsFolder {
		return os.ModeDir | 0o755
	}
	return 0o644 // 可写挂载：常规读写位
}
func (fi fileInfo) Sys() any { return nil }

// ETag 实现 webdav.ETager：返回带引号的 sha256 十六进制。
func (fi fileInfo) ETag(_ context.Context) (string, error) {
	if fi.node.HashSHA256 == nil {
		return "", webdav.ErrNotImplemented
	}
	return `"` + *fi.node.HashSHA256 + `"`, nil
}

// ContentType 实现 webdav.ContentTyper。
func (fi fileInfo) ContentType(_ context.Context) (string, error) {
	if fi.node.MimeType == "" {
		return "", webdav.ErrNotImplemented
	}
	return fi.node.MimeType, nil
}

// rootFileInfo 根目录的虚拟目录信息（根不对应 files 行）。
type rootFileInfo struct{}

func (rootFileInfo) Name() string { return "/" }
func (rootFileInfo) Size() int64  { return 0 }
func (rootFileInfo) IsDir() bool  { return true }
func (rootFileInfo) ModTime() time.Time {
	return time.Time{} // 根无更新时间概念，零值即可
}
func (rootFileInfo) Mode() os.FileMode { return os.ModeDir | 0o755 }
func (rootFileInfo) Sys() any          { return nil }

// --- File 实现 ---

// readFile 包装 *minio.Object（已原生实现 Read/Seek/Close）。
type readFile struct {
	obj io.ReadSeekCloser
	fi  os.FileInfo
}

func (rf *readFile) Stat() (os.FileInfo, error)                 { return rf.fi, nil }
func (rf *readFile) Read(p []byte) (int, error)                 { return rf.obj.Read(p) }
func (rf *readFile) Seek(offset int64, whence int) (int64, error) { return rf.obj.Seek(offset, whence) }
func (rf *readFile) Readdir(int) ([]os.FileInfo, error) {
	return nil, errors.New("webdavfs: 对文件调用 Readdir")
}
func (rf *readFile) Close() error { return rf.obj.Close() }
func (rf *readFile) Write([]byte) (int, error) {
	return 0, ErrWriteUnsupported
}

// dirFile 目录句柄：Readdir 时拉取子节点（过滤未完成上传的中间态）。
type dirFile struct {
	fs     *FS
	uid    int64
	parent *int64 // nil = 根
	fi     os.FileInfo
	listed bool
}

func (df *dirFile) Stat() (os.FileInfo, error) { return df.fi, nil }
func (df *dirFile) Read([]byte) (int, error) {
	return 0, errors.New("webdavfs: 对目录调用 Read")
}
func (df *dirFile) Seek(int64, int) (int64, error) { return 0, nil }
func (df *dirFile) Close() error                   { return nil }
func (df *dirFile) Write([]byte) (int, error) {
	return 0, ErrWriteUnsupported
}

// Readdir 列出全部子节点（is_folder DESC, name ASC）。
// ListByParent 上限 200/页，循环翻页拉全量；跳过非 completed 的中间态文件行。
func (df *dirFile) Readdir(int) ([]os.FileInfo, error) {
	if df.listed {
		return nil, nil
	}
	df.listed = true

	ctx := context.Background()
	page := 1
	var out []os.FileInfo
	for {
		nodes, total, err := df.fs.repos.Files.ListByParent(ctx, df.uid, df.parent, page, 200)
		if err != nil {
			return nil, err
		}
		for i := range nodes {
			if !nodes[i].IsFolder && nodes[i].Status != domain.FileStatusCompleted {
				continue
			}
			out = append(out, newFileInfo(&nodes[i]))
		}
		if page*200 >= total || len(nodes) == 0 {
			break
		}
		page++
	}
	return out, nil
}

// parseDBTime 解析 pgx 扫描出的 timestamptz 字符串。
// domain 层时间列统一为 string，pgx stdlib 文本格式为 PostgreSQL 风格
// （"2026-09-03 12:34:56.789+00"），偏移无冒号；兼容 RFC3339 兜底。
func parseDBTime(s string) time.Time {
	for _, layout := range []string{
		"2006-01-02 15:04:05.999999999-07",
		"2006-01-02 15:04:05.999999999Z07:00",
		time.RFC3339,
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

// 确认 webdav.File 接口在编译期被完整实现。
var (
	_ webdav.FileSystem   = (*FS)(nil)
	_ webdav.File         = (*readFile)(nil)
	_ webdav.File         = (*dirFile)(nil)
	_ os.FileInfo         = fileInfo{}
	_ os.FileInfo         = rootFileInfo{}
	_ webdav.ETager       = fileInfo{}
	_ webdav.ContentTyper = fileInfo{}
	_ BlobStore           = (*storage.MinIO)(nil)
)
