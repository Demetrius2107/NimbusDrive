package quota

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/Demetrius2107/NimbusDrive/internal/domain"
	"github.com/Demetrius2107/NimbusDrive/internal/logger"
	"github.com/Demetrius2107/NimbusDrive/internal/middleware"
	"github.com/Demetrius2107/NimbusDrive/internal/store"
	"github.com/Demetrius2107/NimbusDrive/internal/tracing"
	"github.com/gin-contrib/sse"
	"github.com/gin-gonic/gin"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
)

// heartbeatInterval SSE 心跳间隔。防止反向代理因空闲超时关闭连接。
const heartbeatInterval = 30 * time.Second

// SSEHandler 处理配额实时推送的 SSE 端点。
type SSEHandler struct {
	notifier *Notifier
	quotas   *store.QuotaRepo
	users    *store.UserRepo
}

// NewSSEHandler 构造。
func NewSSEHandler(notifier *Notifier, quotas *store.QuotaRepo, users *store.UserRepo) *SSEHandler {
	return &SSEHandler{notifier: notifier, quotas: quotas, users: users}
}

// Stream GET /api/v1/quota/stream（SSE 长连接，JWT 保护）
//
// 推送流程：
//  1. 绕过 WriteTimeout（http.NewResponseController 取消单连接写超时）
//  2. Last-Event-ID 补发：客户端重连时浏览器自动带此头，服务端从 Redis Stream 补发
//  3. 初始快照：推当前配额状态
//  4. 订阅 Redis pub/sub + 心跳循环
func (h *SSEHandler) Stream(c *gin.Context) {
	userID := c.GetInt64(middleware.CtxUserID)

	// Redis 不可用时不可降级（SSE 无意义），返回 503。
	if h.notifier == nil || h.notifier.client == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"code":    string(domain.CodeInternal),
			"message": "推送服务暂不可用",
		})
		return
	}

	// 绕过 WriteTimeout：取消该连接的写超时，保持长连接。
	rc := http.NewResponseController(c.Writer)
	_ = rc.SetWriteDeadline(time.Time{})

	// 设置 SSE 响应头。
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no") // Nginx 不缓冲

	ctx := c.Request.Context()

	// 1. Last-Event-ID 补发：客户端断线重连时补发错过的变更。
	if lastIDStr := c.GetHeader("Last-Event-ID"); lastIDStr != "" {
		if n, err := parseVersion(lastIDStr); err == nil && n > 0 {
			h.replayEvents(c, ctx, n, userID)
		}
	}

	// 2. 推送初始快照（当前配额状态）。
	h.sendSnapshot(c, ctx, userID)

	// 3. 订阅 Redis pub/sub。
	pubsub := h.notifier.client.Subscribe(ctx, ChannelQuotaChanges)
	defer func() { _ = pubsub.Close() }()
	msgCh := pubsub.Channel()

	// 4. 心跳 + 事件循环。
	ticker := time.NewTicker(heartbeatInterval)
	defer ticker.Stop()

	c.Stream(func(w io.Writer) bool {
		select {
		case msg, ok := <-msgCh:
			if !ok {
				return false // pubsub 关闭
			}
			var evt QuotaChangeEvent
			if err := json.Unmarshal([]byte(msg.Payload), &evt); err != nil {
				return true // 跳过格式错误的消息
			}
			// 过滤：广播或目标用户匹配
			if evt.TargetUserID != 0 && evt.TargetUserID != userID {
				return true
			}
			// 从事件提取 trace context，起 linked span 续接管理员变更的 trace。
			// SSE 连接是多路复用的（一条连接推 N 个管理员变更），不能用连接级 span，
			// 每条消息独立起 span。
			msgCtx := tracing.ExtractFromTraceParent(ctx, evt.TraceParent)
			_, span := tracing.Tracer("quota.sse").Start(msgCtx, "SSE.push."+string(evt.Type),
				trace.WithSpanKind(trace.SpanKindConsumer),
			)
			// 使用 sse.Event 一并写入 event/id/data，id=version 供客户端 Last-Event-ID 重连。
			_ = sse.Encode(w, sse.Event{
				Event: "quota",
				Id:    strconv.FormatInt(evt.Version, 10),
				Data:  evt,
			})
			span.End()
			return true
		case <-ticker.C:
			_ = sse.Encode(w, sse.Event{Event: "heartbeat", Data: ""})
			return true
		case <-ctx.Done():
			return false // 客户端断开
		}
	})
}

// sendSnapshot 推送当前配额状态作为初始事件。
func (h *SSEHandler) sendSnapshot(c *gin.Context, ctx context.Context, userID int64) {
	user, err := h.users.GetByID(ctx, userID)
	if err != nil {
		logger.L.Warn("sse snapshot: get user failed", zap.Int64("user_id", userID), zap.Error(err))
		return
	}
	snapshot := gin.H{
		"storage_quota": user.StorageQuota,
		"used_storage":  user.UsedStorage,
	}
	// 尝试读取当月传输配额（失败不阻塞）
	if h.quotas != nil {
		if qp, err := h.quotas.GetOrCreateCurrent(ctx, userID); err == nil {
			snapshot["upload_bytes"] = qp.UploadBytes
			snapshot["upload_quota"] = qp.UploadQuota
			snapshot["download_bytes"] = qp.DownloadBytes
			snapshot["download_quota"] = qp.DownloadQuota
			snapshot["period"] = qp.Period
		}
	}
	_ = sse.Encode(c.Writer, sse.Event{Event: "snapshot", Data: snapshot})
	c.Writer.Flush()
}

// replayEvents 补发 version > sinceVersion 的变更。
func (h *SSEHandler) replayEvents(c *gin.Context, ctx context.Context, sinceVersion, userID int64) {
	events, err := h.notifier.ReplaySince(ctx, sinceVersion, userID)
	if err != nil {
		logger.L.Warn("sse replay failed", zap.Int64("since", sinceVersion), zap.Error(err))
		return
	}
	for _, evt := range events {
		_ = sse.Encode(c.Writer, sse.Event{
			Event: "quota",
			Id:    strconv.FormatInt(evt.Version, 10),
			Data:  evt,
		})
	}
	c.Writer.Flush()
}

// parseVersion 解析 Last-Event-ID 为版本号。
func parseVersion(s string) (int64, error) {
	return strconv.ParseInt(s, 10, 64)
}
