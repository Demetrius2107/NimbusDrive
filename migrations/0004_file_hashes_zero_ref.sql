-- 0004: file_hashes 加 zero_ref_at 墓碑列
-- 修复引用计数 GC 的可达性设计缺陷：原 DecrRef 在 ref_count 降为 0 时立即 DELETE 行，
-- 导致 ListZeroRef（WHERE ref_count=0）永远查不到行——物理对象一旦 orphan 就无迹可循，
-- GC 在结构上不可能。改为保留零引用行并记墓碑时间，GC cron 按 grace 窗口回收。
-- 幂等：可重复执行。

ALTER TABLE file_hashes ADD COLUMN IF NOT EXISTS zero_ref_at TIMESTAMPTZ;

-- GC 候选索引：只索引 ref_count=0 且有墓碑的行，cron 扫描走索引。
CREATE INDEX IF NOT EXISTS idx_file_hashes_gc_candidate
  ON file_hashes(zero_ref_at) WHERE ref_count = 0 AND zero_ref_at IS NOT NULL;

COMMENT ON COLUMN file_hashes.zero_ref_at IS
  'ref_count 降为 0 的时刻；GC cron 按 grace 窗口回收物理对象后删行。NULL 表示仍被引用或从未孤立';
