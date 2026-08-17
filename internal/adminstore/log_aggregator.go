// Package adminstore 操作日志异步聚合器。
//
// 设计目标：管理操作产生的审计日志不阻塞主流程、不因日志写入失败影响业务。
//
// 工作流程：
//  1. AdminHandler 调 Record(entry) → 非阻塞推入带缓冲 channel（~1µs）
//  2. 后台 goroutine 从 channel 读取，累积到 batch
//  3. batch 达到 batchSize 或 flushInterval 到期 → 批量 INSERT
//  4. Close() 关闭 channel → goroutine 排空剩余 batch → wg.Wait 优雅关闭
//
// 背压策略：channel 满时丢弃日志（管理操作优先于日志完整性），原子计数器记录丢弃数。
package adminstore

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Demetrius2107/NimbusDrive/internal/logger"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// LogAggregator 异步批量写入操作日志。
type LogAggregator struct {
	db            *gorm.DB
	ch            chan OperationLog
	batchSize     int
	flushInterval time.Duration

	droppedCount uint64 // 原子计数：因 channel 满被丢弃的日志数

	wg   sync.WaitGroup
	stop chan struct{}
}

// NewLogAggregator 构造并启动后台聚合 goroutine。
//   - bufSize: channel 缓冲区大小（建议 512~2048）
//   - batchSize: 批量 INSERT 阈值（建议 50~100）
//   - flushInterval: 定时刷新间隔（建议 3~10s），保证低频操作也能及时落库
func NewLogAggregator(db *gorm.DB, bufSize, batchSize int, flushInterval time.Duration) *LogAggregator {
	la := &LogAggregator{
		db:            db,
		ch:            make(chan OperationLog, bufSize),
		batchSize:     batchSize,
		flushInterval: flushInterval,
		stop:          make(chan struct{}),
	}
	la.wg.Add(1)
	go la.run()
	return la
}

// Record 非阻塞推入一条日志。channel 满时丢弃并返回 false。
// 调用方不应在此阻塞——管理操作的响应延迟优先于日志完整性。
func (la *LogAggregator) Record(entry OperationLog) bool {
	select {
	case la.ch <- entry:
		return true
	default:
		atomic.AddUint64(&la.droppedCount, 1)
		return false
	}
}

// DroppedCount 返回因背压被丢弃的日志总数（可用于监控/告警）。
func (la *LogAggregator) DroppedCount() uint64 {
	return atomic.LoadUint64(&la.droppedCount)
}

// Close 优雅关闭：关闭 channel → goroutine 排空剩余 batch → 等待退出。
// 调用后不应再调 Record。main.go defer 调用。
func (la *LogAggregator) Close() {
	close(la.stop)
	la.wg.Wait()
}

// run 后台聚合循环。select channel 数据 + 定时刷新 + 停止信号。
func (la *LogAggregator) run() {
	defer la.wg.Done()

	batch := make([]OperationLog, 0, la.batchSize)
	ticker := time.NewTicker(la.flushInterval)
	defer ticker.Stop()

	flush := func() {
		if len(batch) == 0 {
			return
		}
		la.flushBatch(batch)
		batch = batch[:0] // 复用底层数组
	}

	for {
		select {
		case entry, ok := <-la.ch:
			if !ok {
				// channel 已关闭（不应发生，关闭走 stop 通道），排空后退出
				flush()
				return
			}
			batch = append(batch, entry)
			if len(batch) >= la.batchSize {
				flush()
			}

		case <-ticker.C:
			flush()

		case <-la.stop:
			// 优雅关闭：排空 channel 中剩余的日志，再刷新 batch
			la.drain(&batch)
			flush()
			return
		}
	}
}

// drain 非阻塞地排空 channel 中剩余的日志到 batch。
func (la *LogAggregator) drain(batch *[]OperationLog) {
	for {
		select {
		case entry, ok := <-la.ch:
			if !ok {
				return
			}
			*batch = append(*batch, entry)
		default:
			return
		}
	}
}

// flushBatch 批量写入一批日志。失败只记录 warning，不重试（避免无限堆积）。
func (la *LogAggregator) flushBatch(batch []OperationLog) {
	if len(batch) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := la.db.WithContext(ctx).CreateInBatches(batch, len(batch)).Error; err != nil {
		dropped := atomic.AddUint64(&la.droppedCount, uint64(len(batch)))
		logger.L.Warn("批量写入操作日志失败，丢弃本批",
			zap.Int("batch_size", len(batch)),
			zap.Uint64("total_dropped", dropped),
			zap.Error(err),
		)
		return
	}
	if dropped := la.DroppedCount(); dropped > 0 {
		logger.L.Debug("操作日志批量写入成功",
			zap.Int("batch_size", len(batch)),
			zap.Uint64("total_dropped", dropped),
		)
	}
}
