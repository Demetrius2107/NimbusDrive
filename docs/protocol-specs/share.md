# 分享协议

> APIServer :8080，路由分散在 `/files/:id/share`（创建，JWT）、`/shares`（管理，JWT）、`/s/:id`（公开访问，无 JWT）。

## 端点

| 方法 | 路由 | 鉴权 | 用途 |
|------|------|------|------|
| POST | `/files/:id/share` | JWT + 契约 | 创建分享 |
| GET | `/shares` | JWT | 我的分享列表 |
| DELETE | `/shares/:id` | JWT | 取消分享 |
| GET | `/s/:id` | 公开 | 查看分享（元数据） |
| POST | `/s/:id/validate` | 公开 + 契约 | 校验访问密码 |

## 1. 创建分享 — POST /files/:id/share

**请求体**（契约 `share.create`）：

| 字段 | 类型 | 约束 | 说明 |
|------|------|------|------|
| `password` | string | ≤ 64 | 可选访问密码，空串等同不设 |
| `expire_time` | string | RFC3339/date-time | 可选过期时间，省略永不过期 |
| `max_access_count` | integer | ≥ 1 | 可选最大访问次数，省略不限次 |

**流程**：
1. 契约中间件校验请求体格式（含 `expire_time` 是否合法 RFC3339）。
2. handler 校验文件归属、非文件夹、未删除（语义校验）。
3. 生成 16 字符 share ID（base32，去除歧义字符 0/O/1/I，来自 10 随机字节）。
4. 密码 bcrypt 哈希（`bcrypt.DefaultCost`）。
5. `expire_time` 解析为时间戳；**未来时间**校验由 handler 负责（语义）。
6. 写入 `shares` 表。

**响应** `ShareDTO`：`{id, user_id, file_id, has_password, expires_at, max_access, access_count, status, created_at}`。

**状态码**：200 / 400 / 403 / 404 / 500。

## 2. 列表 — GET /shares

**Query**：`page`（默认 1）、`page_size`（默认 50）。返回当前用户的分享列表。

## 3. 取消 — DELETE /shares/:id

校验归属后置 `status=cancelled`，同时删除 Redis 缓存（`share:{id}`）。

## 4. 查看 — GET /s/:id（公开）

**响应** `PublicShareDTO`：`{id, file_id, has_password, expires_at, max_access, access_count, status}`（不含 `user_id`）。

## 5. 校验密码 — POST /s/:id/validate（公开）

**请求体**（契约 `share.validate`）：

| 字段 | 类型 | 约束 | 说明 |
|------|------|------|------|
| `password` | string | ≤ 64 | 访问密码，无密码分享可空 body |

**流程**：
1. 契约中间件：空 body 放行（无密码分享兼容）；非空则校验格式。
2. `fetchShare`：Redis 缓存优先（`share:{id}` Hash + TTL 7 天），未命中查 PG 并写穿缓存。
3. 过期/已取消 → 403。
4. 若 `PasswordHash != nil`：要求非空密码 + `bcrypt.CompareHashAndPassword`，错误 → 401。
5. 原子 `shares.IncrAccess`（到达 `max_access_count` 返回 `ErrForbidden` → 403）。
6. 缓存 `IncrShareAccess`。

**响应**：`{access_allowed:true, access_count_remaining:*int, expires_in:*int64, file_id, download_token, download_url}`。

校验通过后签发**分享下载能力令牌**（capability token）：
- `download_token`：64 字符 hex（32 随机字节），存 Redis `share:dl:{token}`，TTL 5 min（`share_download_token_ttl_sec`）。
- `download_url`：`/api/v1/s/download/{token}`（TransferServer 兑换路径，见 download.md 第 4 节）。
- 令牌单次消费（Lua GET+DEL），防重放。客户端凭令牌兑换预签名直连 MinIO 的 URL。
- Redis 不可用时降级：响应不带 `download_token`/`download_url`（前端降级提示），不阻塞 validate 主流程。

**状态码**：200 / 401（密码错/缺）/ 403（耗尽/过期）/ 404 / 500。

## 6. 分享下载兑换 — GET /s/download/:token

> 由 TransferServer 承载（Hertz，公开端点无 JWT）。详见《下载协议》第 4 节。

凭 `download_token` 兑换预签名直连 MinIO 的 URL。授权决策在 APIServer（本节 validate），资源访问在 TransferServer——capability-based security，令牌即授权凭证，TransferServer 不需要懂分享语义。

## 关键实现细节

- **Share ID**：16 字符 base32，去除 `0`/`O`/`1`/`I`，10 随机字节（`crypto/rand`）。
- **密码**：bcrypt `DefaultCost`，不存明文。
- **缓存**：Redis 写穿（write-through），`share:{id}` Hash，TTL 7 天。访问计数同步递增缓存。
- **过期**：`MarkExpired` 定时任务将过期分享置 `status=expired`。
