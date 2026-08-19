# 存储持久层 GC / 清理协议

> APIServer :8080 控制面。进程内 cron（`time.Ticker` + `Start(ctx) func()`，不引第三方库）。
> 对应分支：feat/persistence-gc-outbox。
> 与 [upload.md](./upload.md) / [quota.md](./quota.md) 互补：本篇覆盖后台清理与事件持久化投递的内部协议。

## 概述

持久层审计发现 6 类遗留，本分支补全 4 个后台 cron + 事务型 outbox，解决两类结构性问题：

| 问题 | 根因 | 解 |
|------|------|----|
| 哈希 GC 在结构上不可能 | `DecrRef` ref_count→0 时立即 `DELETE` 行，`ListZeroRef`（`WHERE ref_count=0`）永远查不到候选 | `zero_ref_at` 墓碑列：DecrRef 记墓碑不删行，GC 扫 `ref_count=0 AND zero_ref_at < now()-grace` |
| 事件 dual-write 丢失 | DB 事务 commit 后经进程内 channel → XAdd，commit 与 XAdd 间崩溃则事件永久丢失 | outbox 表与业务同事务写入；OutboxRelay 轮询 `FOR UPDATE SKIP LOCKED` → XAdd → 回写 `published_at` |

## cron 一览

| cron | 触发（默认） | 批次 | 依赖 | 作用 |
|------|-------------|------|------|------|
| TrashPurge | 6h | 100 | PG | 回收站 30 天物理清理（递归 CTE 子树 + DecrRef + HardDeleteRecursive + 配额回补） |
| HashGC | 6h | 50 | PG + MinIO | 零引用哈希 GC（墓碑 + 24h grace + RemoveObject + 再校验删除） |
| SessionExpiry | 1h | 200 | PG + MinIO | 过期上传会话清理（MarkExpired + 占位文件 HardDelete + AbortMultipartUpload） |
| OutboxRelay | 2s | 100 | PG + Redis | outbox 表轮询投递（XAdd 后回写 published_at） |

全部跑在 APIServer（控制面）；TransferServer 保持纯数据面，不加 cron。与 `MonthlyResetCron` 同生命周期管理。

---

## 1. 哈希引用计数 GC

### 1.1 墓碑 + grace window

`file_hashes` 表加 `zero_ref_at TIMESTAMPTZ`（迁移 0004）：

| 操作 | SQL 行为 |
|------|----------|
| `Upsert`（ref_count++） | `ON CONFLICT DO UPDATE SET ref_count=ref_count+1, zero_ref_at=NULL`（再引用清墓碑，移出 GC 候选） |
| `DecrRef`（ref_count--） | `SET ref_count=ref_count-1, zero_ref_at=CASE WHEN ref_count-1=0 THEN now() ELSE zero_ref_at END WHERE ref_count>0`（**不删行**，ref→0 时记墓碑） |
| `ListZeroRef(before, limit)` | `WHERE ref_count=0 AND zero_ref_at IS NOT NULL AND zero_ref_at < $1 LIMIT $2`（GC 候选） |
| `DeleteIfZeroRef(hash)` | `DELETE WHERE hash=$1 AND ref_count=0`（GC 删对象后调，再校验） |

部分索引：`CREATE INDEX ... WHERE ref_count=0 AND zero_ref_at IS NOT NULL`（迁移 0004）。

### 1.2 安全删除序

```
ListZeroRef(now()-grace, batch)        // 1. 取过期墓碑候选
  → mc.RemoveObject(ObjectKey(hash))   // 2. 先删物理对象（失败则跳过，下轮重试）
  → DeleteIfZeroRef(hash)              // 3. 再删 DB 行（WHERE ref_count=0 再校验）
```

**竞态分析**：
- grace 窗口内并发 re-reference：`Upsert` 的 `zero_ref_at=NULL` 在步骤 1 之后、步骤 3 之前把候选移出，步骤 3 的 `WHERE ref_count=0` 再校验拦截 → 安全。
- 窗口外残存竞态（删除内容→等 24h+→GC 执行的秒级窗口内重传同哈希）：概率可忽略，以长 grace + 监控指标兜底。生产 dedup 存储的务实取舍，非理论完美。

### 1.3 指标

| 指标 | 类型 | 标签 | 含义 |
|------|------|------|------|
| `nimbus_hashgc_objects_reclaimed_total` | Counter | — | 成功回收的对象数 |
| `nimbus_hashgc_objects_failed_total` | Counter | `reason` | 回收失败数（remove_error / delete_error） |

---

## 2. 回收站 30 天物理清理

### 2.1 流程

```
ListExpiredTrash(retentionDays, batch)   // files WHERE deleted_at < now()-30d LIMIT batch
  对每个根节点：
    ListDescendants(root)                 // 递归 CTE 收集子树
    对每个后代文件：DecrRef(hash)          // 批量 DecrRef
    HardDeleteRecursive(root) 或 HardDelete(file)  // 递归 CTE 物理删
    IncrUsedStorage(userID, -size)        // 配额回补
```

递归 CTE（`ListDescendants`/`HardDeleteRecursive`）一次性算子树，批次外层循环控制长事务持锁时长。

### 2.2 指标

| 指标 | 类型 | 含义 |
|------|------|------|
| `nimbus_trashpurge_files_deleted_total` | Counter | 物理清理的文件数 |

---

## 3. 上传会话过期清理

`UploadSessionRepo.MarkExpired()`（标记过期）+ `ListExpired(limit)`（取过期会话）：

```
MarkExpired()                            // UPDATE ... WHERE status='active' AND expires_at<now()
ListExpired(batch)
  对每个：Files.HardDelete(init 占位文件) + mc.AbortMultipartUpload(upload_id) + DeleteByID(id)
```

秒传会话无 `upload_id`，仅删占位文件。

### 指标

| 指标 | 类型 | 标签 | 含义 |
|------|------|------|------|
| `nimbus_sessionexpiry_swept_total` | Counter | `result` | 清理结果（success / partial / failed） |

---

## 4. 事务型 Outbox（dual-write 解）

### 4.1 表结构（迁移 0005）

```sql
CREATE TABLE outbox (
  id           UUID PRIMARY KEY,
  event_type   TEXT NOT NULL,
  payload      JSONB NOT NULL,
  trace_context JSONB,        -- W3C trace context，relay 投递时注入
  occurred_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  published_at TIMESTAMPTZ,   -- NULL=未投递
  attempt      INT NOT NULL DEFAULT 0
);
CREATE INDEX idx_outbox_unpublished ON outbox(published_at) WHERE published_at IS NULL;
```

### 4.2 写入点

业务事务内同事务 `Enqueue`（`sqlx.ExtContext` 接受 tx）：

| 写入点 | 事件 | 事务 |
|--------|------|------|
| `completeTransaction` | `file.uploaded` | file_hashes Upsert + files MarkCompleted + users IncrUsedStorage + outbox Enqueue |
| `instantUpload` | `file.uploaded` | 同上（秒传路径） |

降级：`OutboxRepo` 为 nil（PG 不可用或装配未启用）时，`emitFileUploaded` 回退到 `emitter.Emit`（channel best-effort，非持久）。

### 4.3 Relay 投递

```
tick:
  begin tx
  FetchPending(batch)        // SELECT ... WHERE published_at IS NULL ORDER BY occurred_at LIMIT n FOR UPDATE SKIP LOCKED
  对每条：
    evt := msg.ToEvent()
    emitter.Publish(evt)     // 同步 XAdd，返回 error
    成功 → MarkPublished(id)
    失败 → 回滚 tx（published_at 不写，下轮重发）
  commit tx
```

- **at-least-once**：relay 重发间隔 << 消费端 Redis `SETNX(eventID)` 24h 幂等窗口，故 redelivery 安全。
- **多实例水平扩展**：`FOR UPDATE SKIP LOCKED` 无主并发，无需 leader election。
- **trace 传播**：`Enqueue` 时 `tracing.Inject(ctx)` 写入 `trace_context`；relay 投递的事件由消费端提取起 SpanKindConsumer span。

### 4.4 指标

| 指标 | 类型 | 标签 | 含义 |
|------|------|------|------|
| `nimbus_outbox_relay_published_total` | Counter | — | 投递成功数 |
| `nimbus_outbox_relay_errors_total` | Counter | `reason` | 投递失败数（publish / mark） |
| `nimbus_outbox_pending` | Gauge | — | 未投递消息数（scrape-time 查 `CountPending`） |

---

## 5. 配置

`config.dev.yaml` 的 `cron:` 段（`config.CronConfig`）：

```yaml
cron:
  trash_purge:
    retention_days: 30
    batch_size: 100
    interval_sec: 21600      # 6h
  hash_gc:
    grace_hours: 24
    batch_size: 50
    interval_sec: 21600      # 6h
  session_expiry:
    batch_size: 200
    interval_sec: 3600       # 1h
  outbox:
    batch_size: 100
    interval_sec: 2
```

---

## 6. 装配（APIServer main.go）

PG + MinIO 齐全 → 装配 TrashPurge / HashGC / SessionExpiry；PG + emitter 齐全 → 装配 OutboxRelay。`defer cron.Start(ctx)()` 接管生命周期。降级路径：依赖缺失时对应 cron 不装配，日志告警。

## 7. 已知 Gap（留后续）

1. **MinIO list-and-reconcile 全量对账 GC**：当前 GC 依赖 `file_hashes` 行追踪引用，无法发现行已丢失但对象残留的情况；后续运维分支做全量对账。
2. **outbox 事件 schema 版本化**：当前 `event_type` 为字符串，无版本字段；schema registry 留后续。
3. **auth/share 写入点未接 outbox**：当前仅 upload 路径（dual-write 风险最高的路径）接 outbox；auth/share 为单表 insert，crash 窗口小，优先级低，留后续。
4. **relay 无死信队列**：`attempt` 字段已埋但未做超限转 DLQ；持续失败的消息会一直重试，需监控 `OutboxRelayErrors` + `OutboxPending` 告警。
