# 配额协议

> APIServer :8080，路由前缀 `/api/v1`。传输配额追踪 + 配额配置实时推送（SSE）。
> 对应分支：feat/quota-streaming。

## 概述

两类配额，语义不同：

| 类型 | 表 | 字段 | 重置 | 计量时机 |
|------|----|------|------|----------|
| 存储配额 | `users` | `storage_quota` / `used_storage` | 持久累计，不重置 | 上传完成（`file.uploaded` 事件 → UploadStatsConsumer 累计 `used_storage`） |
| 月度传输配额 | `quota_periods` | `upload_bytes` / `download_bytes` / `upload_quota` / `download_quota` | 每月一行，月初重置 | 上传：`file.uploaded` 事件 → QuotaConsumer 累计；下载：预签名签发时按文件大小计量 |

`quota_periods` 每用户每月一行，`period` 格式 `YYYY-MM`（UTC）。`upload_quota`/`download_quota` = 0 表示不限。

## 端点

| 方法 | 路由 | 鉴权 | 用途 |
|------|------|------|------|
| GET | `/quota/stream` | JWT（支持 `?token=` query） | SSE 长连接，实时推送配额配置变更 |
| POST | `/admin/users/quota/reset` | JWT + Admin | 批量重置所有普通用户存储配额 |
| PATCH | `/admin/users/:id/quota` | JWT + Admin | 调整单用户配额（推送 `user_quota_updated`） |
| GET | `/auth/me` | JWT | 拉取当前用户存储配额（首屏 + 降级轮询） |

---

## 1. GET /quota/stream — SSE 实时推送

**鉴权**：JWT，经 `GinJWTAuthAllowQuery` 中间件。浏览器原生 `EventSource` 无法设自定义头，故额外接受 `?token=<JWT>` query 作为令牌来源（仅此端点，其他接口只认 `Authorization` 头）。

**响应头**：

| Header | 说明 |
|--------|------|
| `Content-Type: text/event-stream` | SSE |
| `Cache-Control: no-cache` | 禁缓存 |
| `Connection: keep-alive` | 长连接 |
| `X-Accel-Buffering: no` | Nginx 不缓冲 |

**WriteTimeout 绕过**：`http.NewResponseController(c.Writer).SetWriteDeadline(time.Time{})` 取消该连接的写超时，不影响全局 `WriteTimeout`。

### 事件类型

#### `snapshot` — 初始快照（建连后立即推送）

```json
{
  "storage_quota": 10737418240,
  "used_storage": 1610612736,
  "upload_bytes": 52428800,
  "upload_quota": 10737418240,
  "download_bytes": 104857600,
  "download_quota": 10737418240,
  "period": "2026-08"
}
```

#### `quota` — 配额配置变更（增量推送）

```json
{
  "version": 42,
  "target_user_id": 0,
  "type": "reset_all",
  "payload": { "storage_quota": 10737418240, "affected": 128 },
  "timestamp": "2026-08-19T10:30:00Z"
}
```

- `version`：递增版本号（Redis INCR），同时作为 SSE event `id`
- `target_user_id`：0 = 广播所有用户；非 0 = 定向推送
- `type`：`reset_all` / `user_quota_updated` / `user_status_updated`
- 客户端收到后应重新拉 `GET /auth/me` 拿最新配额值（事件只通知"变了"，不带全量）

#### `heartbeat` — 心跳（每 30s）

空数据，仅保活，防止反向代理空闲超时关连接。

### 可靠性：版本号 + Last-Event-ID 补发

- 每条 `quota` 事件带递增 `version`，SSE event `id = version`
- 客户端断线重连时浏览器原生 `EventSource` 自动带 `Last-Event-ID` 头
- 服务端从 Redis Stream（`nimbus:quota:events`）补发 `version > lastID` 的变更（SSE 协议内置"至少一次"语义）
- 补发只返回广播（`target_user_id=0`）或目标用户匹配的事件

### 跨实例广播

```
管理员改配额 → Notifier.NotifyChange
  → Redis INCR nimbus:quota:version（版本号）
  → XADD nimbus:quota:events（持久化补发）
  → PUBLISH nimbus:quota:changes（实时扇出）
  → 各 APIServer 实例的 SSE 连接订阅 → 推各自的在线客户端
```

单实例可跑，多实例天然扇出。

### 降级

- Redis 不可用：SSE 端点返回 **503**，前端 `EventSource.onerror` 退化为 30s 轮询 `GET /auth/me`
- 连续失败超阈值（2 次）：停止自动重连，切轮询降级

---

## 2. POST /admin/users/quota/reset — 批量重置配额

**鉴权**：JWT + AdminOnly。

**请求体**：

```json
{ "quota": 10737418240 }
```

`quota` 必填，非负整数。

**行为**：
- `UPDATE users SET storage_quota = $1 WHERE is_admin = false`（仅普通用户）
- 广播 `reset_all` 变更事件（`target_user_id=0`）给所有在线客户端
- 写审计日志 `admin.user.quota.reset_all`
- 推送失败不阻塞管理操作（配额已落库，客户端下次刷新/轮询也能拿到最新值）

**响应**：

```json
{
  "code": "0",
  "message": "ok",
  "data": { "affected": 128, "quota": 10737418240 }
}
```

---

## 3. 传输配额校验

### 上传

- `CheckHash` 前置检查月度上传配额（`QuotaRepo.CheckUpload`，只检查不扣）
- 上传完成后 `file.uploaded` 事件 → `QuotaConsumer` → `QuotaRepo.IncrUpload`（原子条件 UPDATE 累计）
- 超限返回 413 `quota_exceeded`

### 下载（预签名）

- 预签名签发时 `QuotaRepo.IncrDownload` 按文件大小计量（授权即扣）
- 分享下载的下载字节计入**文件所有者**配额（非访问者）
- 超限返回 413 `quota_exceeded`

> **MVP 简化**：预签名 URL 让客户端直连 MinIO，TransferServer 无法精确计量实际下载字节。MVP 按签发的文件大小计量（授权即扣全额）。精确计量留后续 MinIO 事件通知优化。

### 原子条件 UPDATE 模式

```sql
UPDATE quota_periods SET upload_bytes = upload_bytes + $2
WHERE user_id = $1 AND period = $3
  AND (upload_quota = 0 OR upload_bytes + $2 <= upload_quota)
```

`affected = 0` → 超限 → `ErrQuotaExceeded`。无需预扣/回退。

---

## 4. 月度重置

`quota.MonthlyResetCron`：进程内 `time.Ticker` 每 6 小时检查是否进入新月。

- 检测到 `period` 变化 → `QuotaRepo.ResetMonthlyAll`：为所有 `status=1` 用户批量创建当月行
- `INSERT ... SELECT FROM users WHERE status=1 ON CONFLICT DO NOTHING`（幂等）
- 进程重启后若已错过月初，下次 tick 补建当月行（幂等）
- `upload_quota`/`download_quota` 从 `users.storage_quota` 派生（MVP：传输配额 = 存储配额）

## 前端集成

- `useQuotaStream` hook（AppLayout 统一挂载）：首屏 `fetchMe` → `EventSource` 建连 → `snapshot`/`quota` 事件 → 连续失败降级 30s 轮询
- `useQuotaStore`（Zustand）：存储/月度传输用量 + 连接状态 + `lastVersion`
- QuotaPage：存储配额圆环 + 月度上传/下载传输条 + 连接状态标签（实时/重连中/轮询）
- AppLayout 侧栏：迷你配额条接真实数据
