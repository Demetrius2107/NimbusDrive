// 分享下载兑换 handler：公开端点，凭能力令牌兑换预签名直连 URL。
// 授权决策在 APIServer（ValidateShare 校验密码/过期/次数后签发令牌），
// 资源访问在 TransferServer（兑换令牌 → 查 files 表 → 签发 MinIO 预签名 URL）。
// 这是 capability-based security：令牌即授权凭证，TransferServer 不需要懂分享语义。
package handler

import (
	"context"
	"encoding/hex"
	"errors"

	"github.com/Demetrius2107/NimbusDrive/internal/cache"
	"github.com/Demetrius2107/NimbusDrive/internal/domain"
	"github.com/Demetrius2107/NimbusDrive/internal/storage"
	"github.com/Demetrius2107/NimbusDrive/internal/store"
	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/common/utils"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
)

// fileGetter 抽象文件查询，便于单测 mock（避免依赖 *store.Repositories）。
type fileGetter interface {
	GetByID(ctx context.Context, id int64) (*domain.FileNode, error)
}

// tokenRedeemer 抽象令牌兑换，便于单测 mock（避免依赖 *cache.Redis）。
type tokenRedeemer interface {
	RedeemShareDownloadToken(ctx context.Context, token string) (cache.ShareDownloadToken, bool, error)
}

// presigner 抽象预签名 URL 签发，便于单测 mock（避免依赖 *storage.MinIO）。
type presigner interface {
	PresignedDownloadURL(ctx context.Context, storagePath string, expire int, filename, mimeType string) (string, error)
}

// downloadQuotaChecker 抽象下载配额扣减，便于单测 mock（避免依赖 *store.QuotaRepo）。
type downloadQuotaChecker interface {
	IncrDownload(ctx context.Context, userID, delta int64) error
}

// ShareDownloadHandler 处理 TransferServer 的分享下载兑换端点（公开，无 JWT）。
type ShareDownloadHandler struct {
	files            fileGetter
	mc               presigner
	rc               tokenRedeemer
	quotas           downloadQuotaChecker
	presignExpireSec int
}

// NewShareDownloadHandler 构造 ShareDownloadHandler。
func NewShareDownloadHandler(repos *store.Repositories, mc *storage.MinIO, rc *cache.Redis, presignExpireSec int) *ShareDownloadHandler {
	return &ShareDownloadHandler{files: repos.Files, mc: mc, rc: rc, quotas: repos.Quotas, presignExpireSec: presignExpireSec}
}

// Redeem GET /api/v1/s/download/:token（公开，无 JWT）
// 兑换能力令牌 → 查文件元信息 → 签发直连 MinIO 的预签名 URL。
// 令牌单次消费（Redis Lua GET+DEL），防重放。第二次兑换同令牌 → 404。
func (h *ShareDownloadHandler) Redeem(ctx context.Context, c *app.RequestContext) {
	token := string(c.Param("token"))
	if !validDownloadToken(token) {
		hertzBadRequest(c, "令牌格式无效")
		return
	}

	// Redis 不可用时不可降级：否则等于无授权下载。
	if h.rc == nil {
		c.JSON(consts.StatusServiceUnavailable, utils.H{
			"code":    string(domain.CodeInternal),
			"message": "下载服务暂不可用",
		})
		return
	}

	tok, ok, err := h.rc.RedeemShareDownloadToken(ctx, token)
	if err != nil {
		hertzInternal(c, "兑换下载令牌失败")
		return
	}
	if !ok {
		hertzNotFound(c, "下载令牌无效或已过期")
		return
	}

	// 查文件元信息（令牌即授权凭证，不校验 owner）。
	file, err := h.files.GetByID(ctx, tok.FileID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			hertzNotFound(c, "文件不存在")
			return
		}
		hertzInternal(c, "查询文件失败")
		return
	}
	if file.DeletedAt != nil || file.IsFolder || file.Status != domain.FileStatusCompleted || file.StoragePath == nil || *file.StoragePath == "" {
		hertzNotFound(c, "文件不可用")
		return
	}

	// 月度下载传输配额扣减，计入文件所有者（分享下载的 actor 不是所有者）。
	if h.quotas != nil {
		if err := h.quotas.IncrDownload(ctx, file.UserID, file.Size); err != nil {
			if errors.Is(err, domain.ErrQuotaExceeded) {
				hertzJSON(c, consts.StatusRequestEntityTooLarge, domain.CodeQuotaExceeded, "月度下载配额不足", nil)
				return
			}
			hertzInternal(c, "扣减下载配额失败")
			return
		}
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
			"filename":   file.Name,
			"size":       file.Size,
		},
	})
}

// validDownloadToken 校验令牌格式：64 个 hex 字符（32 字节）。
// 防止恶意输入打 Redis（长度/字符集过滤在打 Redis 之前）。
func validDownloadToken(token string) bool {
	if len(token) != 64 {
		return false
	}
	_, err := hex.DecodeString(token)
	return err == nil
}
