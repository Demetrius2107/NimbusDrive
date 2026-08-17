# 上传协议

> TransferServer :8081，路由前缀 `/api/v1/upload`，受 `HertzJWTAuth` 保护。

## 端点

| 方法 | 路由 | 用途 |
|------|------|------|
| POST | `/check-hash` | 秒传判定 / 创建上传会话 |
| PUT | `/:sessionId/chunks/:index` | 上传单个分块 |
| GET | `/:sessionId` | 查询上传进度（断点续传） |
| POST | `/:sessionId/complete` | 完成合并（finalize） |
| DELETE | `/:sessionId` | 取消上传 |

## 1. check-hash — 秒传判定 / 创建会话

**请求体**（契约 `upload.check-hash`）：

| 字段 | 类型 | 必填 | 约束 |
|------|------|------|------|
| `hash_sha256` | string | 是 | 小写十六进制 64 位（`^[0-9a-f]{64}$`） |
| `size` | integer | 是 | ≥ 1，文件总字节数 |
| `name` | string | 是 | 1–255 字符，handler 会截断至 255 |
| `parent_id` | integer | 否 | ≥ 1，父文件夹 ID，省略为根目录 |

**流程**：
1. 查哈希池（`file_hashes` 表）。
2. 命中 → **秒传**：复用 `storage_path`，创建 `files` 记录（status=completed），原子扣配额。响应 `{instant:true, file_id, hash_sha256}`。
3. 未命中 → 配额预检 → 创建 `files` 占位（status=init）+ `upload_sessions` + MinIO Multipart 初始化。响应 `{instant:false, session_id, file_id, chunk_size:4194304, total_chunks}`。

**关键常量**：`chunkSize = 4 MiB`，`totalChunks = ceil(size / chunkSize)`。

**状态码**：200 成功 / 400 参数错误 / 413 配额超限（`CodeQuotaExceeded`）/ 409 同名冲突 / 500 内部错误。

## 2. 上传分块 — PUT /:sessionId/chunks/:index

**Path 参数**：
- `sessionId` — 上传会话 ID
- `index` — 分块序号，0-based，须 `< session.TotalChunks`

**请求体**：原始字节流（非 JSON），单块 ≤ ~4 MiB，全量读入内存。

**流程**：
1. 校验会话归属（`session.UserID == userID`，否则 403）与会话状态（须 `active`）。
2. `mc.UploadPart(objectKey, uploadID, idx+1, reader, len)` — MinIO part number = `idx+1`（1-based）。
3. `repos.Uploads.MarkChunkUploaded(sessionID, idx, totalChunks)` — 置位会话位图。

**响应**：`{etag, index}`。状态码：200 / 400（index 非法/空 body）/ 403 / 404 / 500。

**注**：分块身份由 path param 决定，**不使用** `X-File-Id`/`X-Chunk-Index`/`Content-Range` 等 header。哈希在 `check-hash` 阶段已建立。

## 3. 查询进度 — GET /:sessionId

**响应**：`{session_id, total_chunks, missing:[...], status}`。`missing` 为未上传分块序号列表，用于断点续传。

## 4. 完成合并 — POST /:sessionId/complete（finalize）

**流程**：
1. 校验所有分块已上传（`MissingChunks` 为空，否则 400）。
2. `mc.ListParts` → 构造 `[]minio.CompletePart`。
3. `mc.CompleteMultipartUpload` — MinIO 服务端拼接，**非本地组装**。
4. 元数据事务 `completeTransaction`：
   - `file_hashes` upsert（ref_count++）
   - `files` 置 status=completed + hash + storage_path + chunk_count + size
   - 原子扣配额：`UPDATE users SET used_storage = used_storage + $2 WHERE id=$1 AND used_storage+$2 <= storage_quota`；0 行 → `ErrQuotaExceeded` → 413 + 回滚
5. `repos.Uploads.Complete(sessionID)`。

**响应**：`{file_id, storage_path, hash_sha256, size}`。

`objectKey = storage.ObjectKey(session.HashSHA256)` — 内容寻址（content-addressed），相同哈希共享同一对象。

## 5. 取消上传 — DELETE /:sessionId

中止 MinIO Multipart + 置会话状态为 `cancelled` + 删除 `files` 占位记录（若存在）。
