# 下载协议

> TransferServer :8081，路由前缀 `/api/v1/download`，受 `HertzJWTAuth` 保护。

## 端点

| 方法 | 路由 | 用途 |
|------|------|------|
| GET | `/:fileId` | 下载文件（支持 Range） |
| HEAD | `/:fileId` | 预检（仅返回头，无 body） |

## 1. GET /:fileId — 下载

**Path 参数**：`fileId`（int64，>0）。

**鉴权与归属**（`validateAndFetch`）：
- 文件未删除、非文件夹、`status==completed`、`StoragePath` 非空
- **owner-only**：`file.UserID == userID`，否则 403

> **Gap**：分享流程（`/s/:id/validate`）返回 `file_id`，但 TransferServer 下载仅校验 owner，**分享无 mediated 下载端点**。当前分享仅授予元数据访问 + 密码校验，下载需文件所有者本人。后续可加 share-token 下载路径。

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

## 状态码

| 状态码 | 说明 |
|--------|------|
| 200 | 全量下载 |
| 206 | Range 命中，部分内容 |
| 400 | fileId 无效 |
| 403 | 非文件所有者 |
| 404 | 文件不存在/已删除 |
| 500 | 内部错误 |
