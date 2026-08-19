package handler

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/Demetrius2107/NimbusDrive/internal/domain"
	"github.com/Demetrius2107/NimbusDrive/internal/middleware"
	"github.com/Demetrius2107/NimbusDrive/internal/storage"
	"github.com/Demetrius2107/NimbusDrive/internal/store"
	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/common/utils"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
	"github.com/minio/minio-go/v7"
)

// DownloadHandler 处理 TransferServer 的下载接口。
// 流式回写（MinIO GetObject → TransferServer → 客户端）与预签名直连（Presign）两种路径并存：
// 预签名为主路径，流式作降级（公网端点未配 / MinIO 直连不通时回退）。
type DownloadHandler struct {
	repos            *store.Repositories
	mc               *storage.MinIO
	presignExpireSec int
}

// NewDownloadHandler 构造 DownloadHandler。
// 下载只需读 files 表 + MinIO，无跨表事务，不需要 db。
// presignExpireSec 控制预签名 URL 有效期（秒）。
func NewDownloadHandler(repos *store.Repositories, mc *storage.MinIO, presignExpireSec int) *DownloadHandler {
	return &DownloadHandler{repos: repos, mc: mc, presignExpireSec: presignExpireSec}
}

// Download GET /api/v1/download/:fileId
// 鉴权 + 权限校验 → MinIO GetObject(range) → 流式回写。
// 支持标准 HTTP Range 头，返回 206 Partial Content。
func (h *DownloadHandler) Download(ctx context.Context, c *app.RequestContext) {
	file, ok := h.validateAndFetch(ctx, c)
	if !ok {
		return
	}

	totalSize := file.Size
	storagePath := *file.StoragePath

	// 解析 Range 头。
	rangeHeader := string(c.Request.Header.Peek("Range"))
	start, end, hasRange := parseRange(rangeHeader, totalSize)

	// 构造 MinIO 请求。
	opts := minio.GetObjectOptions{}
	if hasRange {
		if err := opts.SetRange(start, end); err != nil {
			hertzInternal(c, "构造 Range 请求失败")
			return
		}
	}

	obj, err := h.mc.GetObject(ctx, storagePath, opts)
	if err != nil {
		hertzInternal(c, "获取对象失败")
		return
	}
	// minio.Object 在读取前需检查 Stat 错误（MinIO 的延迟错误返回模式）。
	objInfo, err := obj.Stat()
	if err != nil {
		_ = obj.Close()
		hertzInternal(c, "获取对象元信息失败")
		return
	}

	// 设置响应头。
	c.Header("Content-Type", file.MimeType)
	c.Header("Content-Disposition", storage.BuildContentDisposition(file.Name))
	c.Header("Accept-Ranges", "bytes")

	if hasRange {
		contentLength := end - start + 1
		c.Header("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, totalSize))
		c.Header("Content-Length", strconv.FormatInt(contentLength, 10))
		c.SetStatusCode(consts.StatusPartialContent)
		c.SetBodyStream(obj, int(contentLength))
	} else {
		c.Header("Content-Length", strconv.FormatInt(objInfo.Size, 10))
		c.SetStatusCode(consts.StatusOK)
		c.SetBodyStream(obj, int(objInfo.Size))
	}
}

// Presign GET /api/v1/download/:fileId/presign
// 鉴权 + owner 校验 → 签发直连 MinIO 的预签名 URL，字节流不再过 TransferServer。
// 预签名 URL 的签名覆盖 query 参数，不覆盖 Range 头：客户端可对同一 URL 发多次
// Range 请求做分块下载，无需每块单独签。
// 响应头（Content-Type / Content-Disposition）通过 response-* 查询参数由 MinIO 注入。
func (h *DownloadHandler) Presign(ctx context.Context, c *app.RequestContext) {
	file, ok := h.validateAndFetch(ctx, c)
	if !ok {
		return
	}

	rawURL, err := h.mc.PresignedDownloadURL(ctx, *file.StoragePath, h.presignExpireSec, file.Name, file.MimeType)
	if err != nil {
		hertzInternal(c, "签发下载链接失败")
		return
	}

	c.JSON(consts.StatusOK, utils.H{
		"code":    string(domain.CodeOK),
		"message": "ok",
		"data": utils.H{
			"url":        rawURL,
			"expires_in": h.presignExpireSec,
			"method":     "GET",
		},
	})
}
// 预检：返回文件大小与 Content-Type，不传输 body。
// 客户端据此决定分块下载策略。
func (h *DownloadHandler) Head(ctx context.Context, c *app.RequestContext) {
	file, ok := h.validateAndFetch(ctx, c)
	if !ok {
		return
	}

	c.Header("Content-Type", file.MimeType)
	c.Header("Content-Disposition", storage.BuildContentDisposition(file.Name))
	c.Header("Accept-Ranges", "bytes")
	c.Header("Content-Length", strconv.FormatInt(file.Size, 10))
	c.SetStatusCode(consts.StatusOK)
	// HEAD 不写 body
	_ = io.EOF
}

// validateAndFetch 鉴权 + 校验文件可下载性，返回文件节点。
// 校验失败时已写入错误响应，调用方直接 return。
func (h *DownloadHandler) validateAndFetch(ctx context.Context, c *app.RequestContext) (*domain.FileNode, bool) {
	fileIDStr := c.Param("fileId")
	fileID, err := strconv.ParseInt(fileIDStr, 10, 64)
	if err != nil || fileID <= 0 {
		hertzBadRequest(c, "文件 ID 无效")
		return nil, false
	}

	userID := middleware.HertzUserID(c)

	file, err := h.repos.Files.GetByID(ctx, fileID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			hertzNotFound(c, "文件不存在")
			return nil, false
		}
		hertzInternal(c, "查询文件失败")
		return nil, false
	}

	// 软删除不可下载。
	if file.DeletedAt != nil {
		hertzNotFound(c, "文件不存在")
		return nil, false
	}
	// 文件夹不可下载。
	if file.IsFolder {
		hertzBadRequest(c, "文件夹不可下载")
		return nil, false
	}
	// 只有完成上传的文件可下载。
	if file.Status != domain.FileStatusCompleted {
		hertzBadRequest(c, "文件未完成上传")
		return nil, false
	}
	// 必须有物理存储路径。
	if file.StoragePath == nil || *file.StoragePath == "" {
		hertzNotFound(c, "文件物理存储不存在")
		return nil, false
	}
	// 权限校验：只有文件所有者可下载。
	if file.UserID != userID {
		hertzForbidden(c, "无权访问此文件")
		return nil, false
	}

	return file, true
}

// parseRange 解析 HTTP Range 头。
// 支持三种格式：
//   - bytes=0-       （从 start 到末尾）
//   - bytes=0-99      （指定范围，end 包含）
//   - bytes=-99       （最后 99 字节）
//
// 不支持多段 Range（bytes=0-99,200-）→ 返回 ok=false，调用方走全量。
// 格式非法也返回 ok=false，安全降级为全量下载。
func parseRange(rangeHeader string, totalSize int64) (start, end int64, ok bool) {
	if rangeHeader == "" || totalSize <= 0 {
		return 0, 0, false
	}

	const prefix = "bytes="
	if !strings.HasPrefix(rangeHeader, prefix) {
		return 0, 0, false
	}

	spec := strings.TrimSpace(rangeHeader[len(prefix):])

	// 多段 Range 不支持。
	if strings.Contains(spec, ",") {
		return 0, 0, false
	}

	dashIdx := strings.Index(spec, "-")
	if dashIdx < 0 {
		return 0, 0, false
	}

	startStr := spec[:dashIdx]
	endStr := spec[dashIdx+1:]

	// bytes=-99：最后 99 字节
	if startStr == "" {
		suffixLen, err := strconv.ParseInt(endStr, 10, 64)
		if err != nil || suffixLen <= 0 {
			return 0, 0, false
		}
		if suffixLen > totalSize {
			suffixLen = totalSize
		}
		return totalSize - suffixLen, totalSize - 1, true
	}

	s, err := strconv.ParseInt(startStr, 10, 64)
	if err != nil || s < 0 || s >= totalSize {
		return 0, 0, false
	}

	// bytes=0-：从 start 到末尾
	if endStr == "" {
		return s, totalSize - 1, true
	}

	e, err := strconv.ParseInt(endStr, 10, 64)
	if err != nil || e < s {
		return 0, 0, false
	}
	// end 不能超过文件末尾。
	if e >= totalSize {
		e = totalSize - 1
	}
	return s, e, true
}
