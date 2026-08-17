// 分享 handler：创建/列表/取消/公开访问/校验密码。
// 一个业务模块一个文件：本文件为分享模块（APIServer 侧）。
// 设计：PG 为事实源 + Redis write-through 缓存（share:{id} Hash + TTL）。
package handler

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Demetrius2107/NimbusDrive/internal/cache"
	"github.com/Demetrius2107/NimbusDrive/internal/domain"
	"github.com/Demetrius2107/NimbusDrive/internal/eventbus"
	"github.com/Demetrius2107/NimbusDrive/internal/middleware"
	"github.com/Demetrius2107/NimbusDrive/internal/store"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"golang.org/x/crypto/bcrypt"
)

// 默认缓存 TTL（无过期分享的上限，防止缓存堆积）。
const shareCacheDefaultTTL = 7 * 24 * time.Hour

// ShareHandler 处理分享模块接口。
type ShareHandler struct {
	shares  *store.ShareRepo
	files   *store.FileRepo
	cache   *cache.Redis
	db      *sqlx.DB
	emitter *eventbus.Emitter // 可为 nil
}

// NewShareHandler 构造 ShareHandler。
func NewShareHandler(shares *store.ShareRepo, files *store.FileRepo, cache *cache.Redis, db *sqlx.DB, emitter *eventbus.Emitter) *ShareHandler {
	return &ShareHandler{shares: shares, files: files, cache: cache, db: db, emitter: emitter}
}

// --- 鉴权接口（JWT）---

// CreateShareRequest 创建分享请求体。
type CreateShareRequest struct {
	Password       string `json:"password"`
	ExpireTime     string `json:"expire_time"`      // ISO8601，空则永不过期
	MaxAccessCount *int   `json:"max_access_count"` // nil 则不限次
}

// CreateShare POST /api/v1/files/:id/share — 对指定文件创建分享。
func (h *ShareHandler) CreateShare(c *gin.Context) {
	userID := c.GetInt64(middleware.CtxUserID)

	fileID, ok := parseFileID(c)
	if !ok {
		return
	}

	var req CreateShareRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		abortBadRequest(c, "请求体格式错误")
		return
	}

	// 校验文件归属
	file, err := h.files.GetByID(c.Request.Context(), fileID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			abortNotFound(c, "文件不存在")
			return
		}
		abortInternal(c, "查询文件失败")
		return
	}
	if file.UserID != userID {
		abortForbidden(c, "无权分享他人的文件")
		return
	}
	if file.IsFolder {
		abortBadRequest(c, "暂不支持分享文件夹")
		return
	}
	if file.DeletedAt != nil {
		abortBadRequest(c, "文件已删除")
		return
	}

	// 生成 16 字符短 ID（base32，去除歧义字符）
	shareID, err := generateShareID()
	if err != nil {
		abortInternal(c, "生成分享 ID 失败")
		return
	}

	// 密码哈希
	var passwordHash *string
	if strings.TrimSpace(req.Password) != "" {
		hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
		if err != nil {
			abortInternal(c, "密码处理失败")
			return
		}
		hStr := string(hash)
		passwordHash = &hStr
	}

	// 过期时间
	var expiresAt *string
	if req.ExpireTime != "" {
		t, err := time.Parse(time.RFC3339, req.ExpireTime)
		if err != nil {
			abortBadRequest(c, "expire_time 格式错误，需 ISO8601/RFC3339")
			return
		}
		if t.Before(time.Now()) {
			abortBadRequest(c, "过期时间不能早于当前时间")
			return
		}
		s := t.UTC().Format(time.RFC3339)
		expiresAt = &s
	}

	share := &domain.Share{
		ID:           shareID,
		UserID:       userID,
		FileID:       fileID,
		PasswordHash: passwordHash,
		ExpiresAt:    expiresAt,
		MaxAccess:    req.MaxAccessCount,
	}

	if _, err := h.shares.Create(c.Request.Context(), share); err != nil {
		abortInternal(c, "创建分享失败")
		return
	}

	// write-through 写缓存
	h.cacheShare(c.Request.Context(), share)

	// 发射 share.created 事件
	h.emitShareCreated(share)

	c.JSON(http.StatusOK, gin.H{
		"code":    string(domain.CodeOK),
		"message": "ok",
		"data":    h.toShareDTO(share),
	})
}

// ListShares GET /api/v1/shares — 列出我的分享。
func (h *ShareHandler) ListShares(c *gin.Context) {
	userID := c.GetInt64(middleware.CtxUserID)

	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "50"))

	shares, total, err := h.shares.ListByUser(c.Request.Context(), userID, page, pageSize)
	if err != nil {
		abortInternal(c, "查询分享列表失败")
		return
	}

	dtos := make([]gin.H, 0, len(shares))
	for i := range shares {
		dtos = append(dtos, h.toShareDTO(&shares[i]))
	}

	c.JSON(http.StatusOK, gin.H{
		"code":    string(domain.CodeOK),
		"message": "ok",
		"data": gin.H{
			"shares":    dtos,
			"page":      page,
			"page_size": pageSize,
			"total":     total,
		},
	})
}

// CancelShare DELETE /api/v1/shares/:id — 取消分享。
func (h *ShareHandler) CancelShare(c *gin.Context) {
	userID := c.GetInt64(middleware.CtxUserID)
	shareID := c.Param("id")

	// 校验归属
	share, err := h.shares.Get(c.Request.Context(), shareID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			abortNotFound(c, "分享不存在")
			return
		}
		abortInternal(c, "查询分享失败")
		return
	}
	if share.UserID != userID {
		abortForbidden(c, "无权取消他人的分享")
		return
	}

	if err := h.shares.Cancel(c.Request.Context(), shareID); err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			abortNotFound(c, "分享已取消或不存在")
			return
		}
		abortInternal(c, "取消分享失败")
		return
	}

	// 清缓存（失败不阻塞主流程）
	_ = h.cache.DelShare(c.Request.Context(), shareID)

	c.JSON(http.StatusOK, gin.H{"code": string(domain.CodeOK), "message": "ok"})
}

// --- 公开接口（无 JWT）---

// GetShare GET /api/v1/s/:id — 查看分享详情（不暴露密码）。
func (h *ShareHandler) GetShare(c *gin.Context) {
	shareID := c.Param("id")

	share, err := h.fetchShare(c.Request.Context(), shareID)
	if err != nil {
		h.respondShareError(c, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"code":    string(domain.CodeOK),
		"message": "ok",
		"data":    h.toPublicShareDTO(share),
	})
}

// ValidateShareRequest 校验密码请求体。
type ValidateShareRequest struct {
	Password string `json:"password"`
}

// ValidateShare POST /api/v1/s/:id/validate — 校验密码并自增访问计数。
func (h *ShareHandler) ValidateShare(c *gin.Context) {
	shareID := c.Param("id")

	var req ValidateShareRequest
	_ = c.ShouldBindJSON(&req) // 无密码分享时 body 可为空

	share, err := h.fetchShare(c.Request.Context(), shareID)
	if err != nil {
		h.respondShareError(c, err)
		return
	}

	// 密码校验
	if share.PasswordHash != nil {
		if req.Password == "" {
			abortUnauthorized(c, "请输入密码")
			return
		}
		if err := bcrypt.CompareHashAndPassword([]byte(*share.PasswordHash), []byte(req.Password)); err != nil {
			abortUnauthorized(c, "密码错误")
			return
		}
	}

	// 访问计数 +1（原子，含上限校验）
	if err := h.shares.IncrAccess(c.Request.Context(), shareID); err != nil {
		if errors.Is(err, domain.ErrForbidden) {
			abortForbidden(c, "访问次数已用尽或分享已失效")
			return
		}
		abortInternal(c, "更新访问计数失败")
		return
	}

	// 同步缓存计数（失败不阻塞）
	_ = h.cache.IncrShareAccess(c.Request.Context(), shareID)

	// 发射 share.accessed 事件（公开端点，无登录用户，记录访问者 IP）
	h.emitShareAccessed(shareID, share.FileID, c.ClientIP())

	// 计算剩余次数与过期秒数
	var remaining *int
	if share.MaxAccess != nil {
		left := *share.MaxAccess - (share.AccessCount + 1)
		if left < 0 {
			left = 0
		}
		remaining = &left
	}
	var expiresIn *int64
	if share.ExpiresAt != nil {
		t, _ := time.Parse(time.RFC3339, *share.ExpiresAt)
		sec := int64(time.Until(t).Seconds())
		if sec < 0 {
			sec = 0
		}
		expiresIn = &sec
	}

	c.JSON(http.StatusOK, gin.H{
		"code":    string(domain.CodeOK),
		"message": "ok",
		"data": gin.H{
			"access_allowed":          true,
			"access_count_remaining":  remaining,
			"expires_in":              expiresIn,
			"file_id":                 share.FileID,
		},
	})
}

// --- 辅助 ---

// emitShareCreated 发射 share.created 事件。emitter 为 nil 时静默降级。
func (h *ShareHandler) emitShareCreated(share *domain.Share) {
	if h.emitter == nil {
		return
	}
	payload := map[string]any{
		"share_id":     share.ID,
		"file_id":      share.FileID,
		"has_password": share.PasswordHash != nil,
	}
	if share.ExpiresAt != nil {
		payload["expires_at"] = *share.ExpiresAt
	}
	h.emitter.Emit(&domain.Event{
		ID:         uuid.NewString(),
		Type:       domain.EventShareCreated,
		OccurredAt: time.Now().UTC(),
		ActorID:    share.UserID,
		Payload:    payload,
	})
}

// emitShareAccessed 发射 share.accessed 事件。公开端点无登录用户，ActorID=0。
func (h *ShareHandler) emitShareAccessed(shareID string, fileID int64, accessorIP string) {
	if h.emitter == nil {
		return
	}
	h.emitter.Emit(&domain.Event{
		ID:         uuid.NewString(),
		Type:       domain.EventShareAccessed,
		OccurredAt: time.Now().UTC(),
		ActorID:    0,
		Payload: map[string]any{
			"share_id":    shareID,
			"file_id":     fileID,
			"accessor_ip": accessorIP,
		},
	})
}

// fetchShare 取分享：先查 Redis，miss 回源 PG 并回填缓存。
// 发现过期时标记 expired 并清缓存，返回 domain.ErrNotFound。
func (h *ShareHandler) fetchShare(ctx context.Context, shareID string) (*domain.Share, error) {
	// 1. 查缓存
	cached, err := h.cache.GetShare(ctx, shareID)
	if err == nil && cached != nil {
		share := shareFromCache(cached)
		if h.isExpired(share) {
			_ = h.cache.DelShare(ctx, shareID)
			_ = h.shares.MarkExpired(ctx, shareID)
			return nil, domain.ErrNotFound
		}
		if share.Status == domain.ShareCancelled {
			return nil, domain.ErrNotFound
		}
		return share, nil
	}

	// 2. 回源 PG
	share, err := h.shares.Get(ctx, shareID)
	if err != nil {
		return nil, err
	}

	// 3. 过期判断
	if h.isExpired(share) {
		_ = h.shares.MarkExpired(ctx, shareID)
		return nil, domain.ErrNotFound
	}
	if share.Status == domain.ShareCancelled {
		return nil, domain.ErrNotFound
	}

	// 4. 回填缓存
	h.cacheShare(ctx, share)

	return share, nil
}

// cacheShare 将分享写入 Redis 缓存。
func (h *ShareHandler) cacheShare(ctx context.Context, s *domain.Share) {
	if h.cache == nil {
		return
	}
	fields := map[string]interface{}{
		"id":            s.ID,
		"user_id":       strconv.FormatInt(s.UserID, 10),
		"file_id":       strconv.FormatInt(s.FileID, 10),
		"access_count":  strconv.Itoa(s.AccessCount),
		"status":        string(s.Status),
	}
	if s.PasswordHash != nil {
		fields["password_hash"] = *s.PasswordHash
	}
	if s.ExpiresAt != nil {
		fields["expires_at"] = *s.ExpiresAt
	}
	if s.MaxAccess != nil {
		fields["max_access"] = strconv.Itoa(*s.MaxAccess)
	}

	ttl := shareCacheDefaultTTL
	if s.ExpiresAt != nil {
		if t, err := time.Parse(time.RFC3339, *s.ExpiresAt); err == nil {
			dur := time.Until(t)
			if dur > 0 {
				ttl = dur
			}
		}
	}
	_ = h.cache.SetShare(ctx, s.ID, fields, ttl)
}

// isExpired 判断分享是否已过期。
func (h *ShareHandler) isExpired(s *domain.Share) bool {
	if s.ExpiresAt == nil {
		return false
	}
	t, err := time.Parse(time.RFC3339, *s.ExpiresAt)
	if err != nil {
		return false
	}
	return t.Before(time.Now())
}

// respondShareError 将分享查询错误映射为 HTTP 响应。
func (h *ShareHandler) respondShareError(c *gin.Context, err error) {
	if errors.Is(err, domain.ErrNotFound) {
		abortNotFound(c, "分享不存在、已过期或已取消")
		return
	}
	abortInternal(c, "查询分享失败")
}

// toShareDTO 鉴权用户视角的分享 DTO（含 user_id，不回显密码）。
func (h *ShareHandler) toShareDTO(s *domain.Share) gin.H {
	dto := gin.H{
		"id":            s.ID,
		"user_id":       s.UserID,
		"file_id":       s.FileID,
		"has_password":  s.PasswordHash != nil,
		"expires_at":    s.ExpiresAt,
		"max_access":    s.MaxAccess,
		"access_count":  s.AccessCount,
		"status":        string(s.Status),
		"created_at":    s.CreatedAt,
	}
	return dto
}

// toPublicShareDTO 公开访问视角的分享 DTO（不含 user_id，不回显密码）。
func (h *ShareHandler) toPublicShareDTO(s *domain.Share) gin.H {
	return gin.H{
		"id":           s.ID,
		"file_id":      s.FileID,
		"has_password": s.PasswordHash != nil,
		"expires_at":   s.ExpiresAt,
		"max_access":   s.MaxAccess,
		"access_count": s.AccessCount,
		"status":       string(s.Status),
	}
}

// shareFromCache 从 Redis Hash 字段重建 domain.Share。
func shareFromCache(m map[string]string) *domain.Share {
	s := &domain.Share{
		ID:        m["id"],
		Status:    domain.ShareStatus(m["status"]),
	}
	if v, ok := m["user_id"]; ok {
		s.UserID, _ = strconv.ParseInt(v, 10, 64)
	}
	if v, ok := m["file_id"]; ok {
		s.FileID, _ = strconv.ParseInt(v, 10, 64)
	}
	if v, ok := m["access_count"]; ok {
		s.AccessCount, _ = strconv.Atoi(v)
	}
	if v, ok := m["password_hash"]; ok && v != "" {
		s.PasswordHash = &v
	}
	if v, ok := m["expires_at"]; ok && v != "" {
		s.ExpiresAt = &v
	}
	if v, ok := m["max_access"]; ok && v != "" {
		n, _ := strconv.Atoi(v)
		s.MaxAccess = &n
	}
	return s
}

// generateShareID 生成 16 字符的分享短 ID（base32，去除歧义字符 0/O/1/I）。
func generateShareID() (string, error) {
	// 10 字节随机 → base32 编码约 16 字符
	b := make([]byte, 10)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("read random: %w", err)
	}
	enc := base32.NewEncoding("23456789ABCDEFGHJKLMNPQRSTUVWXYZ").WithPadding(base32.NoPadding)
	return enc.EncodeToString(b), nil
}
