# feat/presigned-download: 预签名下载（客户端直连 MinIO）+ 分享下载能力令牌

> 分支：`feat/presigned-download`，从 master（含 feat/async-events 合入 332fa49）迁出。
> 状态：进行中 | 日期：2026-08-19

## 深挖点

两个缺口合一刀切：

1. **决策 #7（设计文档）**：owner 下载当前走 TransferServer 流式中转（MinIO GetObject → TransferServer → 客户端），大文件吃满 TransferServer 带宽与内存。P2 改**预签名 URL 直连 MinIO**——TransferServer 只签发一次性 URL，字节流不再过服务端。
2. **download.md Gap**：`/s/:id/validate` 返回 `file_id`，但 TransferServer 下载是 owner-only，**分享根本无法下载**。

合一切法：预签名 URL 同时解决 owner 直连与分享下载。难点不在"签 URL"这个动作，而在**分享下载的授权解耦**——APIServer 知道分享语义（密码/过期/次数），TransferServer 有 MinIO 凭证但不知道分享。解法是**能力令牌（capability token）**：APIServer 校验通过后签发一次性短 TTL 令牌存 Redis，TransferServer 兑换令牌 → 签发预签名 URL。这是经典的 capability-based security：授权决策与资源访问分离，令牌即授权凭证。

## 工程化要点（换到 OSS/COS/GCS 一条不少）

- **响应头覆盖**：S3 预签名 URL 支持 `response-content-disposition`/`response-content-type` 查询参数，让浏览器直接以正确文件名保存，无需服务端代理设置 header。
- **公网/内网端点分离**：上传走内网端点（`127.0.0.1:9000`），预签名 URL 必须用客户端可达的公网端点（`minio.example.com`）。用独立的 presign client 构造，内网 client 不变。
- **能力令牌生命周期**：短 TTL（5 min）+ 单次消费（DEL 防重放）+ Redis 持证。令牌只证明"曾被授权"，不携带文件内容；兑换时再查 files 表补 storage_path/mime/name。
- **Range 兼容**：预签名 GET URL 的签名覆盖 query 参数，不覆盖 Range 头——一个预签名 URL 可被客户端发多次 Range 请求做分块下载，无需每块单独签。

## 现状（探索结论）

- `storage/minio.go` 注释明确"仅 TransferServer 直接使用"——**APIServer 不引入 MinIO 客户端**，服务边界不破。
- `MinIOConfig.PresignExpireSec` 字段已存在（yaml 默认 3600s），但无任何 presign 代码。
- APIServer 无 MinIO 依赖；TransferServer 已有 `rc`（cache.Redis，事件总线用）和 `st`（store）。
- `DownloadHandler.validateAndFetch` 是现成的 owner 鉴权 + 文件可下载性校验，owner 预签名端点直接复用。
- `ShareHandler.ValidateShare` 已完成密码/过期/次数校验 + `IncrAccess`，是令牌签发的天然挂载点。
- `parseRange`/`buildContentDisposition`/`isASCII` 是 download_handler 的纯函数，预签名响应头构造复用 `buildContentDisposition`。

## 决策

1. **保留流式下载作降级**：现有 `Download`/`Head` 不删，作为预签名不可用（公网端点未配 / MinIO 直连不通）时的回退。预签名为主路径。
2. **令牌存 Redis**：APIServer 签发，TransferServer 兑换。Redis 不可用时降级——分享下载返回 503（不可降级，否则等于绕过授权），owner 预签名不依赖 Redis（只走 JWT + files 表）。
3. **令牌单次消费**：兑换即 `DEL`，防重放。客户端拿到预签名 URL 后的多次 Range 请求直接打 MinIO，不再回 TransferServer。
4. **响应返回 JSON 不强制 302**：`{url, expires_in, method}`。前端可灵活处理（新窗口打开 / iframe / fetch+blob），比强制 302 更可控。owner 与分享兑换都返回 JSON。
5. **公网端点空则用内网**：dev 环境客户端能直连 MinIO 内网端点；prod 配 `public_endpoint`。

## 范围

### 在范围
- `storage.MinIO` 增加 presign client + `PresignedDownloadURL` 方法（响应头覆盖）
- `MinIOConfig` 增加 `PublicEndpoint`/`PublicUseSSL`
- owner 预签名端点：`GET /api/v1/download/:fileId/presign`（TransferServer，JWT + owner 校验）
- 分享下载能力令牌：APIServer `ValidateShare` 签发 + `cache.Redis` 存取 + TransferServer 兑换
- 分享下载兑换端点：`GET /api/v1/s/download/:token`（TransferServer，公开 + 令牌校验）
- 单测：令牌签发/兑换/重放、presign URL 构造、owner 校验
- 文档：protocol-specs download.md/share.md 更新、设计文档决策 #7、路线图

### 不在范围（显式标注）
- 预签名**上传**（PUT 直传 MinIO）——上传已是分块直传 TransferServer，改造收益小，留后续
- 令牌刷新/续期——5 min TTL 足够，过期重新 validate
- 下载流量计费/限速——留 feat/quota-transmission
- 416 Range Not Satisfiable 收紧——独立行为变更，不在本分支

## 实现方案

### 1. 配置 — `internal/config/config.go`

```go
type MinIOConfig struct {
    // ...现有字段...
    PublicEndpoint    string `mapstructure:"public_endpoint"`     // 客户端可达端点；空则用 endpoint
    PublicUseSSL      bool   `mapstructure:"public_use_ssl"`
    PresignExpireSec  int    `mapstructure:"presign_expire_sec"`   // 已存在
}
```

`config.dev.yaml` 加 `public_endpoint: ""`（dev 用内网）、`public_use_ssl: false`。默认值在 Load 设。

### 2. storage 层 — `internal/storage/minio.go`

MinIO 结构增加 `PresignClient *minio.Client`（用公网端点构造；公网空则等于 Client）：

```go
type MinIO struct {
    Client       *minio.Client
    Core         *minio.Core
    PresignClient *minio.Client  // 用公网端点，签发给客户端的 URL 用这个
    Bucket       string
}
```

`New` 中构造 PresignClient：若 `PublicEndpoint != ""` 用它，否则复用 endpoint。

```go
// PresignedDownloadURL 签发一次性下载 URL，覆盖响应头让浏览器以正确文件名保存。
// storagePath = ObjectKey(hash)；respHeaders 含 content-disposition/content-type。
func (m *MinIO) PresignedDownloadURL(ctx context.Context, storagePath string, expire time.Duration, filename, mimeType string) (string, error) {
    reqParams := url.Values{}
    if mimeType != "" {
        reqParams.Set("response-content-type", mimeType)
    }
    if filename != "" {
        reqParams.Set("response-content-disposition", buildContentDisposition(filename))
    }
    u, err := m.PresignClient.PresignedGetObject(ctx, m.Bucket, storagePath, expire, reqParams)
    if err != nil {
        return "", fmt.Errorf("presigned get: %w", err)
    }
    return u.String(), nil
}
```

`buildContentDisposition`/`isASCII` 从 download_handler 迁到 storage（或共享 util），避免循环依赖——放 storage 包导出。

### 3. owner 预签名端点 — `internal/handler/download_handler.go`

```go
// Presign GET /api/v1/download/:fileId/presign
// 鉴权 + owner 校验 → 签发直连 MinIO 的预签名 URL。
func (h *DownloadHandler) Presign(ctx context.Context, c *app.RequestContext) {
    file, ok := h.validateAndFetch(ctx, c)  // 复用 owner 校验
    if !ok { return }
    expire := time.Duration(h.presignExpireSec) * time.Second
    rawURL, err := h.mc.PresignedDownloadURL(ctx, *file.StoragePath, expire, file.Name, file.MimeType)
    if err != nil { hertzInternal(c, "签发下载链接失败"); return }
    c.JSON(consts.StatusOK, utils.H{
        "code": string(domain.CodeOK),
        "data": utils.H{"url": rawURL, "expires_in": h.presignExpireSec, "method": "GET"},
    })
}
```

`DownloadHandler` 增加 `presignExpireSec int` 字段，`NewDownloadHandler` 加参数。

### 4. 分享下载能力令牌 — APIServer 侧

**令牌格式**：32 字节 `crypto/rand` → hex（64 字符），不可猜。
**Redis 键**：`share:dl:{token}`，TTL 5 min。
**值**：JSON `{share_id, file_id, issued_at}`。

`cache.Redis` 增加两个方法：

```go
func (r *Redis) IssueShareDownloadToken(ctx context.Context, shareID string, fileID int64, ttl time.Duration) (string, error)
func (r *Redis) RedeemShareDownloadToken(ctx context.Context, token string) (shareID string, fileID int64, ok bool, err error)
```

`Redeem` 用 Lua 脚本原子 GET + DEL（单次消费，防并发重放）：

```lua
local v = redis.call('GET', KEYS[1])
if v then redis.call('DEL', KEYS[1]) end
return v
```

`ShareHandler.ValidateShare` 在 `IncrAccess` 成功后签发令牌，响应增加：

```go
"data": gin.H{
    "access_allowed": true,
    "file_id": share.FileID,
    "download_token": token,            // 新增
    "download_url": "/api/v1/s/download/" + token,  // 新增，TransferServer 兑换路径
    // ...existing remaining/expires_in...
}
```

Redis 不可用时（`h.cache == nil` 或 Issue 失败）：返回响应但不带 token 字段（前端可降级提示"下载暂不可用"），不阻塞 validate 主流程。

### 5. 分享下载兑换端点 — TransferServer 侧

新增 `ShareDownloadHandler`（TransferServer，Hertz）：

```go
type ShareDownloadHandler struct {
    repos            *store.Repositories
    mc               *storage.MinIO
    rc               *cache.Redis      // 兑换令牌
    presignExpireSec int
}

// Redeem GET /api/v1/s/download/:token（公开，无 JWT）
func (h *ShareDownloadHandler) Redeem(ctx context.Context, c *app.RequestContext) {
    token := c.Param("token")
    if !validToken(token) { hertzBadRequest(c, "令牌无效"); return }
    shareID, fileID, ok, err := h.rc.RedeemShareDownloadToken(ctx, token)
    if err != nil || !ok { hertzNotFound(c, "下载令牌无效或已过期"); return }
    // 查文件元信息（不校验 owner——令牌即授权凭证）
    file, err := h.repos.Files.GetByID(ctx, fileID)
    if err != nil || file.DeletedAt != nil || file.Status != domain.FileStatusCompleted || file.StoragePath == nil {
        hertzNotFound(c, "文件不可用"); return
    }
    rawURL, err := h.mc.PresignedDownloadURL(ctx, *file.StoragePath, expire, file.Name, file.MimeType)
    if err != nil { hertzInternal(c, "签发下载链接失败"); return }
    c.JSON(consts.StatusOK, utils.H{
        "code": string(domain.CodeOK),
        "data": utils.H{"url": rawURL, "expires_in": h.presignExpireSec, "method": "GET",
                        "filename": file.Name, "size": file.Size},
    })
}
```

`validToken`：长度 64 + hex 字符校验，防恶意输入打 Redis。
**降级**：`h.rc == nil` → 503（不可降级，否则等于无授权下载）。

### 6. 路由接入

**TransferServer `cmd/transferserver/main.go`**：
```go
download.GET("/:fileId/presign", dh.Presign)  // 新增，在 GET /:fileId 之前注册避免路由冲突
// 分享下载兑换：公开端点，无 JWT
if st != nil && mc != nil && rc != nil {
    sh := handler.NewShareDownloadHandler(st.Repos(), mc, rc, cfg.MinIO.PresignExpireSec)
    v1.GET("/s/download/:token", sh.Redeem)
}
```

**APIServer `cmd/apiserver/main.go`**：无路由变更（ValidateShare 内部签发令牌）。

### 7. 单测

**`internal/storage/minio_test.go`**（若 presign 依赖实连 MinIO，测 `buildContentDisposition`/`isASCII` 纯函数 + ObjectKey）。
**`internal/handler/share_download_handler_test.go`**：
- 令牌格式校验（非法长度/非 hex → 400）
- nil Redis → 503
- 兑换成功路径（mock Redis 返回 token payload + mock Files.GetByID + mock MinIO）
- 重放：第二次兑换同令牌 → 404（mock Redis DEL 后返回 ok=false）
- 文件不可用（已删除/未完成/无 storage_path）→ 404
**`internal/handler/download_handler_test.go`**：
- owner 预签名：非 owner → 403；文件不存在 → 404；成功 → JSON 含 url/expires_in

用接口抽象 Redis 与 MinIO 依赖以便 mock（参考 quota_reconcile_consumer 的 redisClient 接口模式）。

### 8. 文档

- `docs/protocol-specs/download.md`：新增「3. 预签名下载」节（owner `GET /:fileId/presign`）；更新 Gap 节标注"已由预签名 + 能力令牌解决"。
- `docs/protocol-specs/share.md`：更新「5. 校验密码」响应增加 `download_token`/`download_url`；新增「6. 分享下载兑换」节。
- `docs/设计文档_v1.md` 决策 #7：`MVP 流式` → `✅ 预签名 URL 直连 MinIO（流式保留作降级）`。
- `docs/开发路线图_v1.md`：新增 feat/presigned-download 条目。

## 验证标准

- `GOARCH=amd64 go vet ./...` + `go build ./...` + `go test ./...` 通过
- `tsc --noEmit` 通过（前端无改动，回归确认）
- owner 链路：`GET /download/:fileId/presign` → 返回 MinIO 直连 URL → 客户端 GET URL 带 Range → 206 分块
- 分享链路：`POST /s/:id/validate` → 返回 token → `GET /s/download/:token` → 返回 MinIO URL → 重放同 token → 404
- 降级：Redis 不可用时 owner 预签名仍可用（不依赖 Redis）；分享兑换返回 503
- 服务边界：APIServer 不引入 MinIO 依赖
- 拆分 Conventional Commits，本地 `--no-ff` 合入 master

## Commit 拆分（预期）

1. `feat(config): MinIO 公网端点 + presign 配置`
2. `feat(storage): PresignClient + PresignedDownloadURL（响应头覆盖）`
3. `feat(handler): owner 预签名下载端点（TransferServer）`
4. `feat(cache): 分享下载能力令牌签发/兑换（Redis + Lua 单次消费）`
5. `feat(handler): 分享下载兑换端点 + ValidateShare 签发令牌`
6. `test: presign/token 签发兑换重放单测`
7. `docs: protocol-specs + 设计文档决策 #7 + 路线图`
