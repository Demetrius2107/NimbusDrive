// Package webdavfs 把 NimbusDrive 的 FileRepo / MinIO 桥接为
// golang.org/x/net/webdav.FileSystem，供 TransferServer /dav 挂载点使用。
// 设计文档：docs/protocol-specs/webdav.md（决策 D3 路径解析、D4 PUT 写入、D5 删除语义）。
//
// W1 落地只读挂载：Stat / OpenFile(R) / Readdir 可用。
// W2 落地可写挂载：PUT（D4 全流程）+ MKCOL + DELETE + MOVE，见 write.go。
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

// ErrWriteUnsupported 表示只读句柄不支持写操作（写打开走 writeFile，不会到这里）。
var ErrWriteUnsupported = errors.New("webdavfs: 只读句柄不支持写操作")

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
	return p.resolveSegments(ctx, uid, segments)
}

// ResolveParent 拆出父目录与末端名（写操作用）。
// 返回末端名对应的父节点：用户根目录下返回 (nil, 末端名, nil)（根是虚拟目录，恒为目录）；
// 路径本身是根时返回 os.ErrInvalid（根不可作为写操作对象）。父目录不存在返回
// os.ErrNotExist（webdav.Handler 映射为 409）。调用方需检查父节点 IsFolder——
// 父是文件时同样以 os.ErrNotExist 拒绝（PUT /a.docx/b.txt 语义上父不存在）。
func (p *PathResolver) ResolveParent(ctx context.Context, uid int64, name string) (*domain.FileNode, string, error) {
	segments, err := splitSegments(name)
	if err != nil {
		return nil, "", err
	}
	if len(segments) == 0 {
		return nil, "", os.ErrInvalid // 根不可写
	}
	if len(segments) == 1 {
		return nil, segments[0], nil // 用户根目录直接挂末端名
	}
	parentNode, err := p.resolveSegments(ctx, uid, segments[:len(segments)-1])
	if err != nil {
		return nil, "", err
	}
	return parentNode, segments[len(segments)-1], nil
}

// resolveSegments 逐段 GetChildByName 解析已解码的段列表，空切片表示根（nil, nil）。
func (p *PathResolver) resolveSegments(ctx context.Context, uid int64, segments []string) (*domain.FileNode, error) {
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
