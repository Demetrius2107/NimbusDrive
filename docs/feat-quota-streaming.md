# feat/quota-streaming: 传输配额追踪 + 配额配置实时推送（SSE）

> 分支：`feat/quota-streaming`，从 master（含 feat/presigned-download 合入 0608f86）迁出。
> 状态：进行中 | 日期：2026-08-19

## 深挖点

两个缺口合一刀切：

1. **传输配额追踪（设计文档决策 #3）**：现有只有存储配额（`storage_quota`/`used_storage`，持久累计不重置）。决策 #3 早就指出"若需月度**传输**配额，另建 `quota_periods(uid, period, bytes_used)` 表"。本分支补上这张表 + 上传/下载字节计量 + 月度重置语义。
2. **配额配置实时推送**：管理员改配额规则后，用户客户端实时感知——不是等下次刷新 `GET /auth/me`，而是服务端主动推。用户明确场景："服务端把所有用户的配额全部重置，用户能收到"。

深挖不在"发个 SSE"这个动作，而在**推送工程的可靠性**：连接管理（断线自动重连）、版本号防丢失更新（客户端重连后用 `Last-Event-ID` 补漏）、跨实例广播（多 APIServer 实例经 Redis pub/sub 扇出）、降级（SSE 连不上时退化为轮询）。这套模式换到 WebSocket/gRPC stream 一条不少。

## 现状（探索结论）

### 后端
- `users.storage_quota` / `used_storage`：存储配额，持久累计。`GET /auth/me` 返回但**前端从不调用**。
- `store.UserRepo`：`GetByID`/`SetQuota`/`IncrUsedStorage`（原子条件 UPDATE）。无批量重置方法。
- `PATCH /admin/users/:id/quota`：单用户改配额。无批量重置端点。
- 上传字节已计量：`file.uploaded` 事件 → `UploadStatsConsumer` → Redis 临时统计。**下载字节完全未计量**——预签名 URL 让客户端直连 MinIO，TransferServer 不知道下载了多少字节。
- 事件总线：Redis Streams。**无 pub/sub**，但 `rc.Client` 已暴露，可直接 `Subscribe`。
- APIServer（Gin）`WriteTimeout` 有限值（会杀长连接），SSE 端点需绕过。`gin-contrib/sse v0.1.0` 已在依赖图，零新依赖。

### 前端
- axios wrapper + `@tanstack/react-query` v5 已挂载但**零使用**。
- `QuotaPage.tsx`：骨架 `used=0 quota=0`。`AppLayout.tsx`：配额条硬编码 `quotaPercent=16`。
- 无 `EventSource`/`WebSocket`/任何推送代码。Zustand auth store 只存 token/username/isAdmin。

## 决策

1. **SSE 端点放 APIServer（Gin）**：配额配置变更源头在管理端。Gin 已有 `gin-contrib/sse`，零新依赖。
2. **Redis pub/sub 跨实例广播**：管理员改配额 → `PUBLISH` 到 `nimbus:quota:changes` 频道 → 所有 APIServer 实例的 SSE 连接订阅 → 推各自的在线客户端。单实例能跑，多实例天然扇出。
3. **版本号 + Last-Event-ID 防丢失更新**：每条变更事件带递增版本号（`id: <version>`）。SSE 断线重连时浏览器原生 `EventSource` 自动带 `Last-Event-ID` 头，服务端从 Redis Stream 补发 `version > lastID` 的变更。这是 SSE 协议内置的"至少一次"语义。
4. **传输配额追踪**：新建 `quota_periods` 表（`user_id, period, upload_bytes, download_bytes, upload_quota, download_quota, reset_at`）。上传字节：`file.uploaded` 事件 → 新消费者累计。下载字节：预签名 URL 场景下 TransferServer 无法直接计量——MVP **按签发的文件大小计量**（授权即扣下载配额），不做实际字节精确统计（留后续 MinIO 事件通知优化）。
5. **月度重置**：cron 定时任务（每月 1 号）创建新 period 行。`period` 字段格式 `YYYY-MM`。
6. **传输配额校验**：上传 CheckHash 前置检查（只检查不扣，完成时由消费者扣）；预签名下载签发时检查+扣下载配额。
7. **前端**：首次用 `useQuery`/fetch 拉 `GET /auth/me`（补齐骨架），SSE 推送增量更新。断线退化为 30s 轮询。

## 范围

### 在范围
- `quota_periods` 表 + GORM 模型 + 迁移
- `store.QuotaRepo`（sqlx）：当月用量、增减字节、月度重置 + 原子配额校验
- 上传配额校验：CheckHash 前置检查月度传输配额
- 下载配额校验：预签名签发时检查+扣月度下载配额
- 传输配额消费者：`file.uploaded` → 累计 `quota_periods.upload_bytes`
- 批量配额重置端点：`POST /admin/users/quota/reset`
- SSE 端点：`GET /api/v1/quota/stream`（Gin，JWT，长连接）
- Redis pub/sub 桥 + 版本号 + Last-Event-ID 补发 + 心跳
- WriteTimeout 绕过（`http.NewResponseController` 取消单连接写超时）
- 前端：useQuotaStore + EventSource + 降级轮询 + QuotaPage/AppLayout 真实数据
- 单测 + 文档

### 不在范围
- MinIO 事件通知精确计量下载字节（MVP 按授权大小计量）
- 传输配额超限时的实时阻断（只在上传前置/下载签发时检查）
- WebSocket 双向通道（SSE 单向足够）

## 实现方案

### 1. quota_periods 表 + 模型 — `internal/adminstore/quota_period.go`

```go
type QuotaPeriod struct {
    ID            int64
    UserID        int64     `gorm:"uniqueIndex:idx_quota_period_user_period,priority:1"`
    Period        string    `gorm:"size:7;uniqueIndex:idx_quota_period_user_period,priority:2"` // "YYYY-MM"
    UploadBytes   int64     `gorm:"default:0"`
    DownloadBytes int64     `gorm:"default:0"`
    UploadQuota   int64     `gorm:"default:0"`
    DownloadQuota int64     `gorm:"default:0"`
    ResetAt       time.Time
    CreatedAt     time.Time
    UpdatedAt     time.Time
}
```
加入 `adminstore.Migrate` 的 AutoMigrate。

### 2. 传输配额 Repo — `internal/store/quota_repo.go`（sqlx）

- `GetOrCreateCurrent(ctx, userID)` — 获取当月行，不存在则创建
- `IncrUpload(ctx, userID, delta)` — 原子条件 UPDATE，超限返回 `ErrQuotaExceeded`
- `IncrDownload(ctx, userID, delta)` — 同上
- `CheckUpload(ctx, userID, size)` — 只检查不扣（CheckHash 前置用）
- `ResetMonthly(ctx, userID, period, upQuota, dlQuota)` — 创建新月度行

`IncrUpload` SQL（复刻 `IncrUsedStorage` 模式）：
```sql
UPDATE quota_periods SET upload_bytes = upload_bytes + $2
WHERE user_id = $1 AND period = $3
  AND (upload_quota = 0 OR upload_bytes + $2 <= upload_quota)
```

### 3. domain 类型 — `internal/domain/types.go`

`QuotaPeriod` 结构体（db tag，sqlx 用）。

### 4. 上传配额校验 — `upload_handler.go` CheckHash

在现有存储配额校验后增加传输配额检查（只检查不扣）：
```go
if err := h.quotaRepo.CheckUpload(ctx, userID, req.Size); err != nil {
    if errors.Is(err, domain.ErrQuotaExceeded) {
        hertzJSON(c, 413, domain.CodeForbidden, "月度上传配额不足", nil); return
    }
}
```

### 5. 下载配额校验 — `download_handler.go` Presign + `share_download_handler.go` Redeem

预签名签发时 `IncrDownload`（扣下载配额）。分享下载的下载字节计入**文件所有者**的配额。

### 6. 传输配额消费者 — `consumers/quota_consumer.go`

`file.uploaded` → `QuotaRepo.IncrUpload(actorID, size)`。与 `UploadStatsConsumer`（Redis 临时统计）互补：Stats 是热统计 7 天 TTL，QuotaPeriods 是 PG 持久配额月度重置。

### 7. 批量配额重置 — `admin_handler.go`

```go
// POST /api/v1/admin/users/quota/reset
// UPDATE users SET storage_quota = $1 WHERE is_admin = false
// → Notifier.NotifyChange(0, "reset_all", {quota})  广播
// → 审计日志
```

### 8. 配额变更推送 — `internal/quota/` 新包

**`notifier.go`**：
```go
type Notifier struct { client *redis.Client }
func (n *Notifier) NotifyChange(ctx, targetUserID int64, changeType string, payload map[string]any) error
// 版本号：Redis INCR nimbus:quota:version
// 双写：PUBLISH nimbus:quota:changes（实时推）+ XADD nimbus:quota:events（持久化补发）
```

**`sse_handler.go`**：
```go
// GET /api/v1/quota/stream
func (h *QuotaSSEHandler) Stream(c *gin.Context) {
    userID := c.GetInt64(middleware.CtxUserID)
    rc := http.NewResponseController(c.Writer)
    rc.SetWriteDeadline(time.Time{}) // 取消写超时
    c.Header("Content-Type", "text/event-stream")
    c.Header("Cache-Control", "no-cache")
    c.Header("Connection", "keep-alive")
    c.Header("X-Accel-Buffering", "no")

    // Last-Event-ID 补发
    if lastID := c.GetHeader("Last-Event-ID"); lastID != "" {
        h.replaySince(c, lastID, userID)
    }
    // 初始快照
    h.sendSnapshot(c, userID)
    // 订阅 pub/sub + 心跳循环
    pubsub := h.client.Subscribe(ctx, "nimbus:quota:changes")
    ticker := time.NewTicker(30 * time.Second)
    c.Stream(func(w io.Writer) bool {
        select {
        case msg := <-pubsub.Channel():
            // 过滤 + c.SSEvent("quota", evt) with id=version
        case <-ticker.C:
            c.SSEvent("heartbeat", "")
        case <-c.Request.Context().Done():
            return false
        }
        return true
    })
}
```

**降级**：Redis 不可用时 SSE 返回 503；前端 `onerror` 退化为 30s 轮询。

### 9. WriteTimeout 绕过

Go 1.20+ `http.NewResponseController(c.Writer).SetWriteDeadline(time.Time{})` 取消单连接写超时，不影响全局。

### 10. 前端 — `web/src/`

- `stores/quota.ts` — Zustand 配额 store
- `hooks/useQuotaStream.ts` — `useEffect` 首次 fetch + 建 EventSource + onerror 降级轮询
- `lib/api.ts` — 增加 `getMe()` 调 `/auth/me`
- `pages/QuotaPage.tsx` + `layouts/AppLayout.tsx` — 从 `useQuotaStore` 读真实数据替换硬编码

### 11. 月度重置 cron

`time.Ticker` 每 6 小时检查是否月初，是则对所有活跃用户创建当月 `quota_periods` 行。

### 12. 单测
- `quota_repo_test.go`：IncrUpload 超限、GetOrCreateCurrent 幂等（需 PG，标 Skip）
- `notifier_test.go`：序列化、版本号递增（mock Redis）
- `sse_handler_test.go`：初始快照、事件过滤、心跳、客户端断开
- `admin_handler_test.go`：ResetAllQuota（mock repo + notifier）

### 13. 文档
- `docs/protocol-specs/quota.md`（新建）
- `docs/设计文档_v1.md` 决策 #3：`⚠️ 待确认` → `✅ 已实现`
- `docs/开发路线图_v1.md`：新增条目

## 验证标准
- `GOARCH=amd64 go vet ./...` + `go build ./...` + `go test ./...` 通过
- `tsc --noEmit` 通过（web + admin）
- 链路：管理员 `POST /admin/users/quota/reset` → Redis PUBLISH → SSE 推送 → 客户端刷新
- 传输配额：上传完成 → 事件 → QuotaConsumer → quota_periods 累计；下载签发 → IncrDownload → 超限 413
- SSE 断线重连：Last-Event-ID 补发；Redis 不可用 → SSE 503 → 前端轮询降级

## Commit 拆分（预期 10 个）
1. `feat(domain): QuotaPeriod 类型 + quota_periods 表模型 + 迁移`
2. `feat(store): QuotaRepo（当月用量/增减/重置 + 原子配额校验）`
3. `feat(handler): 上传/下载传输配额校验接入`
4. `feat(consumers): 传输配额消费者（file.uploaded → quota_periods）`
5. `feat(admin): 批量配额重置端点 + 月度重置 cron`
6. `feat(quota): Notifier（Redis pub/sub）+ SSE handler（版本号 + Last-Event-ID + 心跳 + 降级）`
7. `feat(api): SSE 路由接入 + WriteTimeout 绕过`
8. `feat(web): useQuotaStore + EventSource + 降级轮询 + QuotaPage/AppLayout 真实数据`
9. `test: quota repo + notifier + sse handler 单测`
10. `docs: protocol-specs quota + 设计文档决策 #3 + 路线图`
