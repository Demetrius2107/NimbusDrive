-- NimbusDrive 初始化 schema（对应《设计文档》v1 第四章 DDL）
-- 执行：psql -f migrations/0001_init.sql
-- 幂等：可重复执行（使用 IF NOT EXISTS / CREATE OR REPLACE）。

-- ===== 枚举类型 =====
DO $$ BEGIN
  CREATE TYPE file_status AS ENUM ('init','uploading','merging','validating','completed','failed','cancelled');
EXCEPTION WHEN duplicate_object THEN NULL; END $$;

DO $$ BEGIN
  CREATE TYPE upload_session_status AS ENUM ('active','completed','aborted','expired');
EXCEPTION WHEN duplicate_object THEN NULL; END $$;

DO $$ BEGIN
  CREATE TYPE share_status AS ENUM ('ready','active','expired','cancelled');
EXCEPTION WHEN duplicate_object THEN NULL; END $$;

-- ===== users =====
CREATE TABLE IF NOT EXISTS users (
  id              BIGSERIAL    PRIMARY KEY,
  username        VARCHAR(64)  NOT NULL UNIQUE,
  email           VARCHAR(255) NOT NULL UNIQUE,
  password_hash   VARCHAR(255) NOT NULL,
  storage_quota   BIGINT       NOT NULL DEFAULT 0,
  used_storage    BIGINT       NOT NULL DEFAULT 0,
  status          SMALLINT     NOT NULL DEFAULT 1,
  is_admin        BOOLEAN      NOT NULL DEFAULT FALSE,
  created_at      TIMESTAMPTZ  NOT NULL DEFAULT now(),
  updated_at      TIMESTAMPTZ  NOT NULL DEFAULT now()
);
COMMENT ON COLUMN users.used_storage IS '已用存储，持久累计；月度传输配额另见表（如启用）';

-- ===== file_hashes =====
CREATE TABLE IF NOT EXISTS file_hashes (
  hash_sha256    CHAR(64)      PRIMARY KEY,
  storage_path   VARCHAR(1024) NOT NULL,
  size           BIGINT        NOT NULL,
  ref_count      INTEGER       NOT NULL DEFAULT 0,
  created_at     TIMESTAMPTZ   NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_file_hashes_ref_zero ON file_hashes(ref_count) WHERE ref_count = 0;

-- ===== files =====
CREATE TABLE IF NOT EXISTS files (
  id             BIGSERIAL     PRIMARY KEY,
  user_id        BIGINT        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  parent_id      BIGINT        REFERENCES files(id) ON DELETE CASCADE,
  name           VARCHAR(255)  NOT NULL,
  size           BIGINT        NOT NULL DEFAULT 0,
  mime_type      VARCHAR(128)  NOT NULL DEFAULT 'application/octet-stream',
  is_folder      BOOLEAN       NOT NULL DEFAULT FALSE,
  hash_sha256    CHAR(64)      REFERENCES file_hashes(hash_sha256),
  chunk_count    INTEGER       NOT NULL DEFAULT 0,
  status         file_status   NOT NULL DEFAULT 'init',
  storage_path   VARCHAR(1024),
  deleted_at     TIMESTAMPTZ,
  created_at     TIMESTAMPTZ   NOT NULL DEFAULT now(),
  updated_at     TIMESTAMPTZ   NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_files_unique_name
  ON files(user_id, parent_id, name) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_files_user_parent ON files(user_id, parent_id) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_files_hash        ON files(hash_sha256) WHERE hash_sha256 IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_files_inprogress  ON files(user_id, status) WHERE status IN ('init','uploading','merging','validating');
CREATE INDEX IF NOT EXISTS idx_files_deleted     ON files(deleted_at) WHERE deleted_at IS NOT NULL;

-- ===== upload_sessions =====
CREATE TABLE IF NOT EXISTS upload_sessions (
  id               UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id          BIGINT       NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  file_id          BIGINT       NOT NULL REFERENCES files(id) ON DELETE CASCADE,
  hash_sha256      CHAR(64)     NOT NULL,
  total_size       BIGINT       NOT NULL,
  chunk_size       INTEGER      NOT NULL DEFAULT 4194304,
  total_chunks     INTEGER      NOT NULL,
  uploaded_chunks  BYTEA,
  status           upload_session_status NOT NULL DEFAULT 'active',
  expires_at       TIMESTAMPTZ  NOT NULL DEFAULT (now() + interval '24 hours'),
  created_at       TIMESTAMPTZ  NOT NULL DEFAULT now(),
  updated_at       TIMESTAMPTZ  NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_upload_sessions_user   ON upload_sessions(user_id, status);
CREATE INDEX IF NOT EXISTS idx_upload_sessions_expire ON upload_sessions(expires_at) WHERE status = 'active';

-- ===== upload_chunks（可选：分块级追踪） =====
CREATE TABLE IF NOT EXISTS upload_chunks (
  session_id     UUID         NOT NULL REFERENCES upload_sessions(id) ON DELETE CASCADE,
  chunk_index    INTEGER      NOT NULL,
  size           BIGINT       NOT NULL,
  etag           VARCHAR(255),
  received_at    TIMESTAMPTZ  NOT NULL DEFAULT now(),
  PRIMARY KEY (session_id, chunk_index)
);

-- ===== shares =====
CREATE TABLE IF NOT EXISTS shares (
  id             VARCHAR(16)  PRIMARY KEY,
  user_id        BIGINT       NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  file_id        BIGINT       NOT NULL REFERENCES files(id) ON DELETE CASCADE,
  password_hash  VARCHAR(255),
  expires_at     TIMESTAMPTZ,
  max_access     INTEGER,
  access_count   INTEGER      NOT NULL DEFAULT 0,
  status         share_status NOT NULL DEFAULT 'ready',
  created_at     TIMESTAMPTZ  NOT NULL DEFAULT now(),
  updated_at     TIMESTAMPTZ  NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_shares_user   ON shares(user_id, status);
CREATE INDEX IF NOT EXISTS idx_shares_expire ON shares(expires_at) WHERE status IN ('ready','active');

-- ===== operation_logs =====
CREATE TABLE IF NOT EXISTS operation_logs (
  id             BIGSERIAL    PRIMARY KEY,
  actor_id       BIGINT,
  actor_type     VARCHAR(16)  NOT NULL,
  action         VARCHAR(64)  NOT NULL,
  target_type    VARCHAR(32),
  target_id      VARCHAR(64),
  ip             INET,
  detail         JSONB,
  created_at     TIMESTAMPTZ  NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_logs_actor  ON operation_logs(actor_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_logs_action ON operation_logs(action, created_at DESC);

-- ===== updated_at 自动维护触发器 =====
CREATE OR REPLACE FUNCTION touch_updated_at() RETURNS TRIGGER AS $$
BEGIN
  NEW.updated_at = now();
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_users_touch            ON users;
CREATE TRIGGER trg_users_touch            BEFORE UPDATE ON users            FOR EACH ROW EXECUTE FUNCTION touch_updated_at();

DROP TRIGGER IF EXISTS trg_files_touch            ON files;
CREATE TRIGGER trg_files_touch            BEFORE UPDATE ON files            FOR EACH ROW EXECUTE FUNCTION touch_updated_at();

DROP TRIGGER IF EXISTS trg_upload_sessions_touch  ON upload_sessions;
CREATE TRIGGER trg_upload_sessions_touch  BEFORE UPDATE ON upload_sessions  FOR EACH ROW EXECUTE FUNCTION touch_updated_at();

DROP TRIGGER IF EXISTS trg_shares_touch           ON shares;
CREATE TRIGGER trg_shares_touch           BEFORE UPDATE ON shares           FOR EACH ROW EXECUTE FUNCTION touch_updated_at();
