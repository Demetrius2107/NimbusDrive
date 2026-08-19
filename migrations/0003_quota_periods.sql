-- 0003: quota_periods 表（用户月度传输配额用量）
-- 消除 DDL 分裂：原本仅由 adminstore/migrate.go 的 GORM AutoMigrate 创建，
-- 现纳入 SQL 迁移作为部署期事实源。GORM AutoMigrate 幂等加列，二者共存不冲突。
-- quota_repo.go 的 ON CONFLICT (user_id, period) 依赖此唯一索引。
-- 幂等：可重复执行。

CREATE TABLE IF NOT EXISTS quota_periods (
  id             BIGSERIAL    PRIMARY KEY,
  user_id        BIGINT       NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  period         VARCHAR(7)   NOT NULL,                              -- "YYYY-MM"
  upload_bytes   BIGINT       NOT NULL DEFAULT 0,
  download_bytes BIGINT       NOT NULL DEFAULT 0,
  upload_quota   BIGINT       NOT NULL DEFAULT 0,                    -- 0 = 不限
  download_quota BIGINT       NOT NULL DEFAULT 0,                    -- 0 = 不限
  reset_at       TIMESTAMPTZ  NOT NULL DEFAULT now(),
  created_at     TIMESTAMPTZ  NOT NULL DEFAULT now(),
  updated_at     TIMESTAMPTZ  NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_quota_period_user_period
  ON quota_periods(user_id, period);

COMMENT ON TABLE quota_periods IS '用户月度传输配额用量，每月一行，period 格式 YYYY-MM';
COMMENT ON COLUMN quota_periods.upload_quota   IS '当月上传配额上限字节，0 表示不限';
COMMENT ON COLUMN quota_periods.download_quota IS '当月下载配额上限字节，0 表示不限';
