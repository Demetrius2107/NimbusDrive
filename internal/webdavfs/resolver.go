// Package webdavfs 把 NimbusDrive 的 FileRepo / MinIO 桥接为
// golang.org/x/net/webdav.FileSystem，供 TransferServer /dav 挂载点使用。
// 设计文档：docs/protocol-specs/webdav.md（决策 D3 路径解析、D5 删除语义）。
//
// W1 范围为只读挂载：Stat / OpenFile(R) / Readdir 可用，
// Mkdir / RemoveAll / Rename 与写打开返回 ErrNotImplemented，W2 落地。
package webdavfs

import (
	"context"
	"errors"
	"net/url"
	"os"
	"strings"

	"github.com/Demetrius2107/NimbusDrive/internal/domain"
	"github.com/Demetrius2107/NimbusDrive/internal/store"
)

// userIDKey 是请求 userID 在 context 中的键。
// webdav.FileSystem 接口签名只传 ctx + 路径，不含用户身份；
// 挂载层（TransferServer）在调用 webdav.Handler 前把 Basic Auth 校验出的
// userID 注入 request context，适配器从这里取回。
type userIDKey struct{}

// WithUserID 把 userID 注入 context（挂载层用）。
func WithUserID(ctx context.Context, userID int64) context.Context {
	return context.WithValue(ctx, userIDKey{}, userID)
}

// userID 从 context 取 userID；缺失返回 ErrUnauthorized（不应发生，挂载层保证）。
func userID(ctx context.Context) (int64, error) {
	v, _ := ctx.Value(userIDKey{}).(int64)
	if v == 0 {
		return 0, ErrUnauthorized
	}
	return v, nil
}

// ErrUnauthorized 表示 context 中没有 userID（挂载层装配错误）。
var ErrUnauthorized = errors.New("webdavfs: context 缺少 user id")

// ErrWriteUnsupported 表示 W1 只读挂载不支持该写操作。
var ErrWriteUnsupported = errors.New("webdavfs: 写操作尚未实现（W2）")

// PathResolver 把 WebDAV 深度路径解析为 files 表节点。
// 邻接表无 path 列，从根逐段 GetChildByName（唯一索引 (user_id, parent_id, name)）。
type PathResolver struct {
	files *store.FileRepo
}

// NewPathResolver 构造 PathResolver。
func NewPathResolver(files *store.FileRepo) *PathResolver {
	return &PathResolver{files: files}
}

// Resolve 把形如 "/文档/项目/计划.docx" 的路径解析为节点。
// 根目录 "/" 返回 (nil, nil)：根不对应任何 files 行（parent_id IS NULL 的虚拟目录）。
// 段做百分号解码；解码后仍含 "/" 或 ".." 的段直接拒绝（名字含 "/" 的存量文件
// 在 WebDAV 语义下不可表达，见设计文档已知 Gap 1）。
func (p *PathResolver) Resolve(ctx context.Context, uid int64, name string) (*domain.FileNode, error) {
	segments, err := splitSegments(name)
	if err != nil {
		return nil, err
	}
	if len(segments) == 0 {
		return nil, nil // 根
	}

	var parent *int64
	var node *domain.FileNode
	for _, seg := range segments {
		child, err := p.files.GetChildByName(ctx, uid, parent, seg)
		if err != nil {
			if errors.Is(err, domain.ErrNotFound) {
				return nil, os.ErrNotExist
			}
			return nil, err
		}
		node = child
		parent = &child.ID
	}
	return node, nil
}

// splitSegments 拆解 WebDAV 路径为已解码的段列表，根返回空切片。
func splitSegments(name string) ([]string, error) {
	clean := strings.Trim(name, "/")
	if clean == "" {
		return nil, nil
	}
	raw := strings.Split(clean, "/")
	segs := make([]string, 0, len(raw))
	for _, r := range raw {
		if r == "" {
			continue
		}
		seg, err := url.PathUnescape(r)
		if err != nil {
			return nil, os.ErrInvalid
		}
		// "/" 是路径分隔符、".." 越权、"." 无意义，均不作为文件名匹配
		if seg == "" || seg == "." || seg == ".." || strings.Contains(seg, "/") {
			return nil, os.ErrNotExist
		}
		segs = append(segs, seg)
	}
	return segs, nil
}

// SegmentsToPath 把段列表编码回 WebDAV href 路径（PROPFIND 响应用）。
// 段内字符按 url.PathEscape 转义，保留 "/" 作分隔符。
func SegmentsToPath(segs []string) string {
	if len(segs) == 0 {
		return "/"
	}
	var b strings.Builder
	for _, s := range segs {
		b.WriteByte('/')
		b.WriteString(url.PathEscape(s))
	}
	return b.String()
}
