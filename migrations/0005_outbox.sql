-- 0005: outbox 表（事务型事件投递，解决 dual-write 问题）
-- 业务事务与事件写入同一事务，OutboxRelay 轮询 published_at IS NULL 的行 XAdd 后回写。
-- at-least-once 投递：消费端已有 Redis SETNX(eventID) 幂等兜底。
-- 多实例 relay：FOR UPDATE SKIP LOCKED 无主并发，各实例取不同行。
-- 幂等：可重复执行。

CREATE TABLE IF NOT EXISTS outbox (
  id            UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
  event_type    VARCHAR(64)  NOT NULL,
  payload       JSONB        NOT NULL,
  trace_context JSONB,                                   -- W3C traceparent+tracestate，跨 Streams 传播
  occurred_at   TIMESTAMPTZ  NOT NULL DEFAULT now(),     -- 业务时间（非投递时间）
  published_at  TIMESTAMPTZ,                             -- NULL=未投递，relay XAdd 成功后回写
  attempt       INTEGER      NOT NULL DEFAULT 0          -- 投递尝试次数，失败递增
);

-- relay 扫描索引：published_at IS NULL 的行按 occurred_at 顺序投递。
CREATE INDEX IF NOT EXISTS idx_outbox_unpublished
  ON outbox(published_at, occurred_at) WHERE published_at IS NULL;

COMMENT ON TABLE outbox IS '事务型 outbox：业务事务同事务写入，relay 轮询投递到 Redis Streams';
COMMENT ON COLUMN outbox.trace_context IS 'W3C trace context，跨 Streams 传播追踪上下文';
COMMENT ON COLUMN outbox.published_at IS 'NULL=未投递；relay XAdd 成功后回写 now()';
