# NimbusDrive 协议规范

> 状态：已启动 | 日期：2026-08-17
> 本目录是 NimbusDrive 双服务（APIServer/TransferServer）的**协议契约**与 **wire 细节**规范。
> 与 `接口文档_v1.md` 互补：后者是面向调用方的 REST 接口清单，本目录补充机器可读契约、wire 约定与已知 gap。

---

## 一、双服务边界

| 服务 | 端口 | 框架 | 职责 |
|------|------|------|------|
| APIServer | :8080 | Gin | 用户/鉴权、管理端、配额、文件元数据 CRUD、分享、文件列表、回收站 |
| TransferServer | :8081 | Hertz | 上传分块接收与合并、下载 Range 直传、上传会话状态机 |

两服务共享同一 JWT secret（同一 `auth.JWTManager`），但 Context 类型不同，中间件各自实现。统一响应体 `{code, message, data}`，错误码见 `domain.ErrorCode`。

## 二、契约文件索引

机器可读契约位于 `internal/contract/schemas/`，draft 2020-12，经 `//go:embed` 编译期嵌入、启动期编译。当前覆盖**请求体**契约：

| 契约 ID | 文件 | 端点 | 说明 |
|---------|------|------|------|
| `share.create` | `share.create.json` | POST /api/v1/files/:id/share | 创建分享请求体 |
| `share.validate` | `share.validate.json` | POST /api/v1/s/:id/validate | 校验分享密码请求体 |
| `upload.check-hash` | `upload.check-hash.json` | POST /api/v1/upload/check-hash | 秒传判定/创建上传会话请求体 |

校验链路：中间件 `GinContract`/`HertzContract` 先用契约拦非法请求体 → handler 再做 `ShouldBindJSON` + 语义校验（归属/配额/未来时间）。两层职责分离。

## 三、协议细节文档

- [upload.md](./upload.md) — 上传协议（check-hash / chunk PUT / status / complete / cancel）
- [download.md](./download.md) — 下载协议（GET/HEAD、Range、当前无 416 的行为、owner-only 限制）
- [share.md](./share.md) — 分享协议（create/validate/get/list/cancel）
- [persistence-gc.md](./persistence-gc.md) — 存储持久层 GC / 清理协议（哈希 GC 墓碑 + grace、回收站清理、会话过期、事务型 outbox）

## 四、已知 Gap（留后续）

以下为现状与理想契约的偏差，已显式记录，不在本分支修复：

1. **Download 无 416**：非法/多 range 静默降级为 200 全量。收紧为 RFC 7233 标准的 416 需行为变更评估。
2. **Share 无下载端点**：`/s/:id/validate` 返回 `file_id`，但 TransferServer 下载仅 owner 可用，分享无 mediated 下载路径。
3. **响应体未契约化**：本分支仅做请求体契约；响应体契约化收益低于请求体，留后续。
4. **未全量迁移**：auth/file/admin 端点仍用 `binding:`/`vd:` tag，未迁到 JSON Schema。先在 share + upload check-hash 落地范式。
