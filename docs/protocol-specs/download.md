# 下载协议

> TransferServer :8081，路由前缀 `/api/v1/download`（受 `HertzJWTAuth` 保护）与 `/api/v1/s/download`（公开，分享下载兑换）。

## 端点

| 方法 | 路由 | 鉴权 | 用途 |
|------|------|------|------|
| GET | `/download/:fileId/presign` | JWT + owner | 签发直连 MinIO 的预签名 URL |
| GET | `/download/:fileId` | JWT + owner | 流式下载（支持 Range，降级路径） |
| HEAD | `/download/:fileId` | JWT + owner | 预检（仅返回头，无 body） |
| GET | `/s/download/:token` | 公开（能力令牌） | 分享下载兑换预签名 URL |

## 1. GET /:fileId — 下载

**Path 参数**：`fileId`（int64，>0）。

**鉴权与归属**（`validateAndFetch`）：
- 文件未删除、非文件夹、`status==completed`、`StoragePath` 非空
- **owner-only**：`file.UserID == userID`，否则 403

> **降级路径**：流式下载（本节）为预签名不可用（公网端点未配 / MinIO 直连不通）时的回退。主路径见「3. 预签名下载」。
>
> **分享下载 Gap 已解决**：分享流程经 `/s/:id/validate` 签发能力令牌，由 `/s/download/:token` 兑换预签名 URL（见「4. 分享下载兑换」）。本 owner 端点仍仅限文件所有者。

### Range 头处理

请求头 `Range: bytes=...`，由 `parseRange` 解析：
- `bytes=0-` — 从开头到结尾
- `bytes=0-99` — 指定区间
- `bytes=-99` — 最后 99 字节
- 多 range（`bytes=0-99,200-`）→ 解析失败 → **降级为 200 全量**
- 非法格式 → **降级为 200 全量**

> **Gap**：当前**不返回 416 Range Not Satisfiable**。非法/多 range 静默降级为 200 全量下载。收紧为 RFC 7233 标准（416 + `Content-Range: bytes */total`）需行为变更评估，留后续。

### 响应头

| Header | 说明 |
|--------|------|
| `Content-Type` | `file.MimeType` |
| `Content-Disposition` | `attachment; filename="..."`（ASCII）或 RFC 5987 `filename*=UTF-8''...`（非 ASCII） |
| `Accept-Ranges` | `bytes` |

### Range 命中（206）

| Header | 说明 |
|--------|------|
| `Content-Range` | `bytes start-end/total` |
| `Content-Length` | `end - start + 1` |

状态码 **206 Partial Content**，body = MinIO `GetObject(range)` 流式直传。

### 无 Range（200）

`Content-Length = objInfo.Size`，状态码 200，body 流式直传。

## 2. HEAD /:fileId — 预检

同 `validateAndFetch` 鉴权。设置 `Content-Type`/`Content-Disposition`/`Accept-Ranges: bytes`/`Content-Length = file.Size`，状态码 200，**无 body**。用于分块下载前预检文件大小与可下载性。

## 3. GET /download/:fileId/presign — 预签名下载（owner 主路径）

签发直连 MinIO 的一次性预签名 URL，字节流不再过 TransferServer。

**鉴权**：JWT + `validateAndFetch`（同流式下载的 owner 校验）。

**流程**：
1. owner 校验通过 → 取 `file.StoragePath` / `file.Name` / `file.MimeType`。
2. `MinIO.PresignedDownloadURL`：通过 `response-content-type` / `response-content-disposition` 查询参数覆盖 MinIO 响应头，让浏览器以正确文件名与 MIME 保存。
3. 用 `PresignClient`（公网端点 `public_endpoint` 构造）签发，URL 客户端可达。公网端点为空时回退内网端点（dev）。

**响应** JSON：
```json
{"code":"OK","message":"ok","data":{"url":"https://minio.example/blobs/...?signature=...","expires_in":3600,"method":"GET"}}
```

**分块下载**：预签名 URL 的签名覆盖 query 参数，**不覆盖 Range 头**。客户端可对同一 URL 发多次 `Range: bytes=lo-hi` 请求做分块下载（206 Partial Content），无需每块单独签。`expires_in` 内有效。

**状态码**：200 / 400 / 403 / 404 / 500。

## 4. GET /s/download/:token — 分享下载兑换（公开）

凭能力令牌兑换预签名 URL。授权决策在 APIServer（`/s/:id/validate` 校验密码/过期/次数后签发令牌），资源访问在 TransferServer（兑换 → 查 files → 签 URL）。capability-based security：令牌即授权凭证。

**Path 参数**：`token`（64 字符 hex，32 随机字节）。

**流程**：
1. `validDownloadToken` 校验格式（64 hex），非法 → 400（防恶意输入打 Redis）。
2. Redis 不可用 → 503（**不可降级**，否则等于无授权下载）。
3. `RedeemShareDownloadToken`：Lua 原子 GET+DEL，单次消费。未命中/已过期/已消费 → 404。
4. 查 files 表（令牌即授权，**不校验 owner**）。已删除/未完成/无 storage_path → 404。
5. 签发预签名 URL（同 owner 路径，含响应头覆盖）。

**响应** JSON：
```json
{"code":"OK","message":"ok","data":{"url":"...","expires_in":3600,"method":"GET","filename":"f.txt","size":1024}}
```

**重放防护**：令牌单次消费，第二次兑换同令牌 → 404。客户端拿到预签名 URL 后的多次 Range 请求直接打 MinIO，不再回 TransferServer。

## 状态码

| 状态码 | 说明 |
|--------|------|
| 200 | 全量下载 / 预签名签发成功 |
| 206 | Range 命中，部分内容 |
| 400 | fileId 无效 / 令牌格式无效 |
| 403 | 非文件所有者 |
| 404 | 文件不存在/已删除 / 令牌无效或已消费 |
| 500 | 内部错误 |
| 503 | 分享下载兑换时 Redis 不可用 |
