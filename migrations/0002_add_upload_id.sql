-- 0002: upload_sessions 增加 upload_id 列
-- 存储 MinIO Multipart Upload 的 uploadID，是会话核心状态（PG 为事实源）。
-- 幂等：可重复执行。

ALTER TABLE upload_sessions ADD COLUMN IF NOT EXISTS upload_id VARCHAR(255);
COMMENT ON COLUMN upload_sessions.upload_id IS 'MinIO Multipart Upload ID，创建会话时由 storage.CreateMultipartUpload 返回';
