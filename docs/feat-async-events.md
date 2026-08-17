# feat/async-events: 事件驱动架构（Redis Streams 事件总线 + 消费者工程化）

> 分支：`feat/async-events`，从 master（含 feat/protocol-specs 合入）迁出。
> 状态：进行中 | 日期：2026-08-17

## 深挖点

从"同步单体"演进到"事件驱动"——TransferServer 完成核心事务后发事件到 Redis Streams，APIServer 内嵌消费者订阅处理。核心不是发消息这个动作，而是**消费者侧的工程化**：至少一次投递、幂等消费、毒丸消息与死信队列、背压、优雅关闭。这套模式换到 RabbitMQ/Kafka 一条不少。

## 现状

- 全项目无任何 stream/event 代码，绿地插入。
- go-redis **v8**（ctx 首参 API），stream 方法：`XAdd`/`XReadGroup`/`XGroupCreateMkStream`/`XAck`/`XDel`/`XPending`/`XClaim`。
- `LogAggregator`（`internal/adminstore/log_aggregator.go`）的 `stop chan + wg.Wait()` 模式是消费者生命周期的现成模板，本分支复刻并强化（增加重试与 DLQ）。
- Redis 降级模式下 `rc` 可能为 nil，emitter/consumer 必须 nil-check + 降级。
- `UploadSession` 缺 `Name`/`ParentID`——`CompleteUpload` post-commit 用 `Files.GetByID` 查询补全。

## 决策

1. **消费者位置**：APIServer 内嵌消费者 goroutine（TransferServer 只 Emit）
2. **消费者业务**：操作日志同步 + 用户日上传量统计 + 配额用量对账 + no-op 骨架（全部实现）
3. **事件载荷补全**：post-commit 查 files 表

## 范围

### 在范围
- 事件总线（`internal/eventbus/`）：Emitter + Consumer 框架
- 领域事件类型（`internal/domain/event.go`）
- 4 类事件发射：`file.uploaded`（含秒传）、`share.created`、`share.accessed`、`user.registered`
- 消费者框架：消费者组 + 至少一次 + 幂等 + DLQ + 优雅关闭
- 3 个业务消费者 + 1 个 no-op 骨架
- 配置（`EventBusConfig`）
- 单测（emitter + 消费者框架纯逻辑）
- 文档 + 路线图

### 不在范围
- 跨进程独立 worker 服务（当前内嵌 APIServer，后续可拆 `cmd/eventworker`）
- 事件回放/重放工具（Streams 支持但非本分支）
- 延迟消息/定时任务（Streams 不原生支持，需额外方案）
- 事件 schema 版本化（先跑通，后续加 `schema_version` 字段）

## 实现方案

### 1. 领域事件类型 — `internal/domain/event.go`

```go
type Event struct {
    ID         string         // 事件唯一 ID（UUID，用于幂等去重）
    Type       string         // 事件类型，如 "file.uploaded"
    OccurredAt time.Time
    ActorID    int64          // 触发用户
    Payload    map[string]any // 类型化载荷
}
```

### 2. Emitter — `internal/eventbus/emitter.go`

- `Emit(evt)` 非阻塞推 channel（复刻 `LogAggregator.Record` 背压模式）
- 后台 goroutine 从 channel 读 → `XADD` 到 `nimbus:events:{type}` 流（按事件类型分 stream）
- `XADD` 用 `MaxLen ~` 近似裁剪防无限增长
- `client == nil` 时降级为 no-op

### 3. Consumer 框架 — `internal/eventbus/consumer.go`（核心深挖点）

- `XREADGROUP` 拉一批（blockMs 阻塞避免空转）
- 处理成功才 `XAck`（至少一次投递）
- 失败超过 maxRetries → 移入 DLQ + XAck 原消息（毒丸隔离）
- 幂等：`SETNX nimbus:processed:{consumer}:{evtID}` 去重
- 优雅关闭：`close(stop)` + `wg.Wait()`
- 消费者重启后 `XAUTOCLAIM` 接管超时未 ack 的消息

### 4. 事件发射点

| 事件 | 文件 | 触发条件 | 载荷 |
|------|------|----------|------|
| `file.uploaded`（正常） | upload_handler.go `CompleteUpload` | `completeTransaction` 成功后 | file_id, user_id, hash, size, storage_path, name, parent_id |
| `file.uploaded`（秒传） | upload_handler.go `CheckHash` | `instantUpload` 返回 nil 后 | 同上，`instant:true` |
| `share.created` | share_handler.go `CreateShare` | `cacheShare` 后 | share_id, user_id, file_id, has_password, expires_at |
| `share.accessed` | share_handler.go `ValidateShare` | `IncrShareAccess` 后 | share_id, file_id, accessor_ip |
| `user.registered` | auth_handler.go `Register` | `users.Create` 成功后 | user_id, username, email |

### 5. 消费者业务（`internal/eventbus/consumers/`）

- **audit_consumer**：订阅全部事件 → 转 `OperationLog` → `LogAggregator.Record`
- **upload_stats_consumer**：订阅 `file.uploaded` → Redis INCRBY 累计字节/文件数
- **quota_reconcile_consumer**：订阅 `file.uploaded` → SUM(files.size) 对比 used_storage，不一致 warn
- **noop_consumer**：骨架模板，只 log

### 6. 配置 — `EventBusConfig`

```go
type EventBusConfig struct {
    StreamPrefix  string // "nimbus:events:"
    ConsumerGroup string // "nimbus-workers"
    BufferSize    int    // 1024
    MaxRetries    int    // 3
    BlockMs       int    // 2000
    DLQPrefix     string // "nimbus:dlq:"
    StreamMaxLen  int64  // 10000
}
```

### 7. 启动接入

- **APIServer**：Redis 可用时建 emitter + 启动 4 个消费者；关闭顺序（LIFO）：消费者 → emitter → Redis
- **TransferServer**：只建 emitter，传给 UploadHandler

## 验证标准

- `GOARCH=amd64 go vet ./...` + `go build ./...` + `go test ./...` 通过
- `tsc --noEmit` 通过
- 链路：上传完成 → `file.uploaded` 入 stream → 消费者落日志 + 统计 + 对账
- 降级：Redis 不可用时 emitter/consumer no-op
- 优雅关闭：SIGTERM 后处理完手上消息再退出
- 幂等：重投递不重复副作用
- 毒丸：永远失败的消息进 DLQ 不阻塞主流

## Commit 拆分

1. `feat(domain): 领域事件类型 Event + 4 类事件常量`
2. `feat(eventbus): Emitter（Redis Streams 生产者 + 背压 + 优雅关闭）`
3. `feat(eventbus): Consumer 框架（消费者组 + 至少一次 + 幂等 + DLQ + 优雅关闭）`
4. `feat(handler): 4 类事件发射点接入`
5. `feat(consumers): 审计/统计/对账/no-op 消费者`
6. `feat(config): EventBusConfig + yaml + 双服务启动接入`
7. `test(eventbus): emitter/consumer 纯逻辑单测`
8. `docs: feat-async-events 计划 + 设计文档决策更新 + 路线图`
