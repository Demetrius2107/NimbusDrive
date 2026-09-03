-- 0006: app_passwords 表（应用专用密码，WebDAV/S3 等协议客户端认证）
-- WebDAV 客户端（Windows 凭据管理器 / rclone / 手机 App）将 Basic Auth 凭据
-- 存储在系统层面，安全边界弱于浏览器。应用密码允许用户按设备/用途发放可吊销凭据，
-- 避免主密码外泄且主密码不可单独更换。
-- WebDAV Basic Auth 仅接受应用密码、拒绝主密码（防止用户形成主密码直连习惯）。
-- 幂等：可重复执行。

CREATE TABLE IF NOT EXISTS app_passwords (
  id            BIGSERIAL PRIMARY KEY,
  user_id       BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  name          VARCHAR(64)  NOT NULL,               -- 用途备注，如 "Windows 笔记本挂载"
  password_hash VARCHAR(255) NOT NULL,               -- bcrypt
  last_used_at  TIMESTAMPTZ,                         -- 懒更新（节流 >1h 才写）
  revoked       BOOLEAN NOT NULL DEFAULT FALSE,      -- 吊销保留行作审计痕迹，不物理删
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_app_passwords_user ON app_passwords(user_id);

COMMENT ON TABLE app_passwords IS '应用专用密码：WebDAV 等协议客户端 Basic Auth 凭据，明文仅创建时返回一次';
