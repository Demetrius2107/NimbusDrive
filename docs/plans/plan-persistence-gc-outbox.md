# feat/persistence-gc-outbox: 存储持久层补全（GC/清理 cron + 事务型 outbox）

> 分支：`feat/persistence-gc-outbox`，从 master（6800a88）迁出。
> 归档时间：2026-08-19
> 动因：持久层审计结论——无 stub/TODO，但 6 类遗留使持久层不完整：dead repo 方法被内联 SQL 绕过、DDL 分裂、DecrRef 与 ListZeroRef 设计冲突致 GC 不可能、三类清理 cron 缺失、领域事件无持久化。

## 深挖点（每点都对应一个非玩具工程决策）

1. **分布式 GC：tombstone + grace window + 安全删除序**
   审计发现 `DecrRef` 在 ref_count 降为 0 时**立即 DELETE 行**（filehash.go:64），导致 `ListZeroRef`（`WHERE ref_count=0`）永远查不到行——物理对象一旦 orphan 就无迹可循，GC 在结构上不可能。这不是"写个 cron"，是**引用计数 GC 的可达性设计**。
   修复：`file_hashes` 加 `zero_ref_at TIMESTAMPTZ` 墓碑列。`DecrRef` 不再删行，ref_count→0 时记 `zero_ref_at=now()`；`Upsert` 再引用时清 `zero_ref_at=NULL`。GC cron 扫 `ref_count=0 AND zero_ref_at < now()-24h`（grace），`RemoveObject(MinIO)` 后 `DELETE WHERE ref_count=0`（再校验）。
   **竞态分析**（文档化）：grace 窗口内并发 re-reference 会被 Upsert 的 `zero_ref_at=NULL` 移出 GC 候选，安全；窗口外的残存竞态（用户删除内容→等 24h+→在 GC 执行的秒级窗口内重传同哈希）概率可忽略，以长 grace + 监控指标兜底——生产 dedup 存储的务实取舍，而非理论完美。

2. **事务型 Outbox：dual-write 问题的标准解**
   现状：`completeTransaction` 提交 DB 事务后，`emitFileUploaded` 经进程内 channel → `XAdd`。进程在 commit 与 XAdd 间崩溃→事件永久丢失（dual-write）。
   解：`outbox` 表与业务写在**同一事务**内写入；`OutboxRelay` goroutine 轮询 `WHERE published_at IS NULL ... FOR UPDATE SKIP LOCKED`，`XAdd` 后回写 `published_at`。at-least-once 投递，消费端已有 Redis `SETNX(eventID)` 幂等（consumer.go:230）兜底，故 redelivery 安全。深点在**把"恰好一次"降级为"至少一次 + 幂等"的工程权衡**，以及 `FOR UPDATE SKIP LOCKED` 做多实例 relay 水平扩展。

3. **递归 CTE 子树批量清理 + 批次化长事务控制**
   回收站 30 天物理清理需对文件夹做：递归 CTE 收集后代文件 hash → 逐个 DecrRef → HardDeleteRecursive。`HardDeleteRecursive`/`ListDescendants` 已用递归 CTE（file.go:274/295），但清理 cron 需在**批次**上做（每批 N 个根节点），避免单事务扫全表持锁过久。深点：递归 CTE 一次性算子树 vs. 批次外层循环的组合。

4. **sqlx.ExtContext 事务感知 repo（executor 接口模式）**
   `completeTransaction` 内联 3 段 SQL 绕过 repo，根因是 repo 方法绑死 `r.db`，无法接受外部 `*sqlx.Tx`。引入 `sqlx.ExtContext`（`*sqlx.DB` 与 `*sqlx.Tx` 都满足）作为 executor 参数，让 `Upsert/DecrRef/ListZeroRef/MarkCompleted/IncrUsedStorage` 在 DB 或 Tx 上均可执行。Go DAL 不靠 DI 框架传播事务的惯用法，使 repo 成为单一 SQL 事实源、可单测，并消除内联 SQL 与 repo 方法的漂移（含 `MarkCompleted` 的 `size=files.size` 自赋值 bug）。

## 决策

1. **crons 全部跑在 APIServer**（控制面），TransferServer 保持纯数据面不加 cron。APIServer 构造 `*storage.MinIO` 客户端供 GC 用（storage 包注释已预期 APIServer 使用 MinIO）。与现有 `MonthlyResetCron` 同侧，统一 cron 生命周期管理。
2. **cron 包**：新建 `internal/cron/`，每个 cron 自包含 struct + `Start(ctx) func()`（与 `MonthlyResetCron` 模式一致，不引第三方 cron 库）。`TrashPurgeCron`/`HashGCCron`/`SessionExpiryCron`/`OutboxRelay` 四个。
3. **DecrRef 行为变更**（破坏性，但当前 DecrRef 仅 1 个 caller）：不再立即 DELETE 零引用行，改写 `zero_ref_at` 墓碑。迁移 `0004` 加列。`Upsert` 的 `ON CONFLICT` 分支补 `zero_ref_at=NULL`。
4. **outbox 写入点**：`completeTransaction`（上传完成）、`share_handler.CreateShare`、`auth_handler.Register` 的 emit 点改为"同事务写 outbox"。`OutboxRelay` 取代当前 `emitFileUploaded→channel→XAdd` 的非持久路径用于这些事件；`Emitter` 的 channel 路径保留给非事务型 best-effort 事件（如 SSE 通知）。
5. **quota_periods DDL 纳入 SQL 迁移**（`0003_quota_periods.sql`，`CREATE TABLE IF NOT EXISTS`），与 GORM AutoMigrate 并存（AutoMigrate 幂等加列，不冲突），SQL 成为部署期事实源。
6. **批次大小可配**：`config.Cron` 段新增各 `BatchSize`/`Interval`/`HashGCGraceHours`（默认 24）。默认值保守（trash 6h、gc 6h、expiry 1h、outbox 2s）。
7. **GC 失败不阻塞**：单个对象 RemoveObject 失败记指标+日志，行不删（下轮重试）；relay 单条 XAdd 失败回滚 tx（`published_at` 不写），下轮重发。
8. **不删 `upload_chunks` 表**：决策 #6 明确 deferred，本分支不启用分块级追踪。
9. **outbox 消费幂等复用现有 Redis SETNX**：不新引入去重表。relay 的 at-least-once 与 consumer 的 24h 幂等窗口对齐（relay 重发间隔 << 24h）。

## 范围

### 在范围
- `sqlx.ExtContext` executor 接口化：`FileHashRepo.Upsert/DecrRef/ListZeroRef`、`FileRepo.MarkCompleted`、`UserRepo.IncrUsedStorage`
- `completeTransaction` 改调 repo 方法（修复 `MarkCompleted` size 自赋值 bug，消除内联 SQL）
- 迁移 `0003_quota_periods.sql`、`0004_file_hashes_zero_ref.sql`、`0005_outbox.sql`
- `DecrRef` 墓碑化 + `Upsert` 清 `zero_ref_at`
- `internal/cron/trash_purge.go`：30 天物理清理（递归 CTE 子树 + 批量 DecrRef + HardDeleteRecursive + 配额回补）
- `internal/cron/hash_gc.go`：零引用哈希 GC（tombstone + 24h grace + MinIO RemoveObject + 安全删除序）
- `internal/cron/session_expiry.go`：上传会话过期清理（`MarkExpired` 接线 + 过期会话的 init 占位文件 HardDelete + AbortMultipartUpload）
- `internal/cron/outbox_relay.go`：outbox 轮询 relay（`FOR UPDATE SKIP LOCKED` + XAdd + 回写 published_at）
- `internal/store/outbox_repo.go`：`OutboxRepo`（`Enqueue`/`FetchPending`/`MarkPublished`）
- outbox 写入点接线：`completeTransaction`、`share_handler.CreateShare`、`auth_handler.Register`
- `config.Cron` 段 + `config.dev.yaml`
- APIServer `main.go`：构造 `*storage.MinIO` + 装配 4 个 cron + metrics 新仪器
- `metrics`：`HashGCObjectsReclaimed`/`HashGCObjectsFailed`/`TrashPurgeFilesDeleted`/`SessionExpirySwept`/`OutboxRelayPublished`/`OutboxRelayErrors`/`OutboxPending`
- 单测：repo executor 接口、DecrRef tombstone、GC 删除序竞态模拟（fake MinIO）、outbox relay 往返、trash 批次
- 文档：`docs/protocol-specs/persistence-gc.md` + 设计文档决策 #10 + 路线图

### 不在范围
- `upload_chunks` 表启用（决策 #6 deferred）
- MinIO 对象级 list-and-reconcile 全量对账 GC（后续运维分支）
- outbox 事件 schema registry / 版本化
- 多 relay 实例 leader election（`FOR UPDATE SKIP LOCKED` 已支持无主并发）
- 前端运维视图（cron 状态走 metrics，Grafana 可观测）

## 实现方案

### 1. 迁移文件（migrations/）
- `0003_quota_periods.sql`：`CREATE TABLE IF NOT EXISTS quota_periods (...)` + 唯一索引 `(user_id, period)`。
- `0004_file_hashes_zero_ref.sql`：`ALTER TABLE file_hashes ADD COLUMN IF NOT EXISTS zero_ref_at TIMESTAMPTZ` + 部分索引 `WHERE ref_count=0 AND zero_ref_at IS NOT NULL`。
- `0005_outbox.sql`：`CREATE TABLE IF NOT EXISTS outbox (id UUID PK, event_type, payload JSONB, trace_context JSONB, occurred_at, published_at, attempt)` + `idx_outbox_unpublished`。

### 2. repo executor 接口化（internal/store/）
- `Upsert(ctx, ext sqlx.ExtContext, hash, storagePath, size)`：SQL 补 `zero_ref_at=NULL` on conflict。
- `DecrRef(ctx, ext, hash)`：`UPDATE ... SET ref_count=ref_count-1, zero_ref_at=CASE WHEN ref_count-1=0 THEN now() ELSE zero_ref_at END WHERE hash=$1 AND ref_count>0`，**不删行**。
- `ListZeroRef(ctx, ext, before time.Time, limit int)`：`WHERE ref_count=0 AND zero_ref_at IS NOT NULL AND zero_ref_at < $1 LIMIT $2`。
- `DeleteIfZeroRef(ctx, ext, hash)`：`DELETE WHERE hash=$1 AND ref_count=0`（GC 删对象后调）。
- `MarkCompleted(ctx, ext, id, hash, storagePath, chunkCount, size)`：`SET ... size=$6`（修 bug，加 size 参数）。
- `IncrUsedStorage(ctx, ext, userID, delta)`：ext 化。
- 现有 caller（`file_handler.go:383` DecrRef）改传 `h.db`。

### 3. completeTransaction 重构（upload_handler.go:456-487）
改调 `h.repos.Hashes.Upsert(ctx, tx, ...)` + `h.repos.Files.MarkCompleted(ctx, tx, ...)` + `h.repos.Users.IncrUsedStorage(ctx, tx, ...)` + `h.outbox.Enqueue(ctx, tx, ...)`。`emitFileUploaded` 的 channel 路径删除（事件改由 relay 发）；metrics 埋点保留。

### 4. cron 包（internal/cron/）
统一模式：`Start(ctx) func()`、tick 内 `repos + batch + metrics + log`、nil 依赖降级返回空 stop。
- `trash_purge.go`：每 tick `SELECT id FROM files WHERE deleted_at < now()-30d LIMIT batch`，对每个：`ListDescendants`→`DecrRef(each)`→`HardDeleteRecursive/HardDelete`→`IncrUsedStorage(userID, -size)` 配额回补。
- `hash_gc.go`：`ListZeroRef(now-grace, batch)`→逐个 `mc.RemoveObject(storage.ObjectKey(hash))`→`DeleteIfZeroRef(hash)`。RemoveObject 失败：inc `HashGCObjectsFailed`，跳过（下轮重试）。
- `session_expiry.go`：`Uploads.MarkExpired()`（已有）+ 新 `ListExpired(limit)`→对每个：`Files.HardDelete(init 占位)` + `mc.AbortMultipartUpload`（若 upload_id 非空）。
- `outbox_relay.go`：`OutboxRepo.FetchPending(tx, batch)`（`FOR UPDATE SKIP LOCKED`）→逐个 `emitter.Publish(evt)`（新同步 XAdd 方法）→`MarkPublished`。失败回滚 tx。

### 5. OutboxRepo + relay 接线
- `internal/store/outbox_repo.go`：`Enqueue(ctx, ext, eventType, payload, traceCtx)`、`FetchPending(ctx, limit)`（`FOR UPDATE SKIP LOCKED`）、`MarkPublished(ctx, id)`、`IncAttempt`、`CountPending(ctx)`（供 metrics）。
- `eventbus.Emitter` 加 `Publish(ctx, evt) error`（同步 XAdd）；保留 `Emit`（channel best-effort）给 SSE 等。
- `UploadHandler`/`ShareHandler`/`AuthHandler` 注入 `*store.OutboxRepo`；emit 点改 `outbox.Enqueue` 同事务写入。

### 6. config + main.go
- `config.Cron` struct + `config.dev.yaml` 的 `cron:` 段。
- `cmd/apiserver/main.go`：`storage.New` → `mc != nil && st != nil` 时构造 4 cron 并 `Start(ctx)`，defer stop。cron 指标接入 `metrics`。

### 7. metrics 新仪器
`collector.go` 加：`HashGCObjectsReclaimed`、`HashGCObjectsFailed{reason}`、`TrashPurgeFilesDeleted`、`SessionExpirySwept{result}`、`OutboxRelayPublished`、`OutboxRelayErrors{reason}`、`OutboxPending`（Gauge，scrape-time 查 `CountPending`）。infra collector 扩展注入 `OutboxMetrics` 接口。

## 验证标准
- `GOARCH=amd64 go vet ./...` + `go build ./...` + `go test ./...` 通过
- `tsc --noEmit` 通过（前端无改）
- 迁移幂等：重复跑 `0003/0004/0005` 不报错
- 手动：起 APIServer（PG+Redis+MinIO via deploy/infra compose）→ 造零引用哈希 → 等 grace（测试用 1s override）→ 观察 MinIO 对象被删、`file_hashes` 行被删、`nimbus_hashgc_objects_reclaimed_total` +1
- outbox：上传完成 → kill APIServer 在 relay 前 → 重启 → 事件仍被投递（验证持久化）
- `completeTransaction` 用 repo 方法后，`files.size` 正确写入（MarkCompleted bug 修复验证）

## Commit 拆分（预期 10 个）
1. `refactor(store): sqlx.ExtContext executor 接口化事务感知 repo`
2. `refactor(upload): completeTransaction 改用 repo 方法，修复 MarkCompleted size 自赋值 bug`
3. `fix(migrations): quota_periods DDL 纳入 SQL 迁移 0003`
4. `feat(store): file_hashes zero_ref_at 墓碑列 + DecrRef 保留零引用行（迁移 0004）`
5. `feat(cron): 回收站 30 天物理清理 cron（递归 CTE 子树 + 批量 DecrRef + 配额回补）`
6. `feat(cron): 零引用哈希 GC cron（tombstone + grace + MinIO 回收 + 竞态分析）`
7. `feat(cron): 上传会话过期清理 cron（MarkExpired 接线 + 占位文件 + AbortMultipart）`
8. `feat(eventbus): outbox 表 + OutboxRepo + relay（dual-write 解决，迁移 0005）`
9. `feat(api): APIServer 接入 MinIO + cron 装配 + outbox 写入点接线 + metrics 仪器`
10. `docs: persistence-gc protocol-specs + 设计文档决策 #10 + 路线图`
