package eventbus

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Demetrius2107/NimbusDrive/internal/domain"
	"github.com/Demetrius2107/NimbusDrive/internal/logger"
	"github.com/Demetrius2107/NimbusDrive/internal/tracing"
	"github.com/go-redis/redis/v8"
	"go.uber.org/zap"
)

// Emitter 是事件总线生产者。业务 handler 调 Emit 非阻塞推送事件，
// 后台 goroutine 批量 XADD 到 Redis Streams（按事件类型分 stream）。
//
// 背压策略：channel 满时丢弃事件并计数（业务响应优先于事件完整性）。
// 降级：client == nil（Redis 不可用）时 Emit 为 no-op，不阻塞业务。
type Emitter struct {
	client       *redis.Client
	streamPrefix string
	maxLen       int64 // 每条 stream 近似上限（XADD MAXLEN ~）

	ch        chan *domain.Event
	droppedCount uint64 // 原子计数：因 channel 满被丢弃的事件数

	wg   sync.WaitGroup
	stop chan struct{}
}

// NewEmitter 构造并启动后台投递 goroutine。client 为 nil 时返回降级实例（Emit no-op）。
func NewEmitter(client *redis.Client, streamPrefix string, bufSize int, maxLen int64) *Emitter {
	e := &Emitter{
		client:       client,
		streamPrefix: streamPrefix,
		maxLen:       maxLen,
		ch:           make(chan *domain.Event, bufSize),
		stop:         make(chan struct{}),
	}
	if client != nil {
		e.wg.Add(1)
		go e.run()
	}
	return e
}

// Emit 非阻塞推送一条事件。channel 满或 Redis 不可用时返回 false。
// 调用方不应在此阻塞——业务响应延迟优先于事件投递。
//
// ctx 用于提取 W3C trace context 注入 evt.TraceContext，使事件跨 Redis Streams
// 传播追踪上下文。Consumer 解码后提取，起 consumer span 续接 trace。
// evt 已有 TraceContext 时不覆盖（调用方预置优先）。
func (e *Emitter) Emit(ctx context.Context, evt *domain.Event) bool {
	if e == nil || e.client == nil {
		return false
	}
	// 注入 trace context（无 active span 时为 nil，omitempty 不占字段）。
	if evt.TraceContext == nil && ctx != nil {
		evt.TraceContext = tracing.Inject(ctx)
	}
	select {
	case e.ch <- evt:
		return true
	default:
		atomic.AddUint64(&e.droppedCount, 1)
		return false
	}
}

// DroppedCount 返回因背压被丢弃的事件总数。
func (e *Emitter) DroppedCount() uint64 {
	return atomic.LoadUint64(&e.droppedCount)
}

// Close 优雅关闭：停止接收新事件 → 排空 channel 中已入队事件 → 等待 goroutine 退出。
// 调用后不应再调 Emit。main.go defer 调用，且必须在 rc.Close() 之前（LIFO）。
func (e *Emitter) Close() {
	if e == nil || e.client == nil {
		return
	}
	close(e.stop)
	e.wg.Wait()
}

// run 后台投递循环。从 channel 读事件 → XADD 到对应 stream。
func (e *Emitter) run() {
	defer e.wg.Done()

	for {
		select {
		case evt, ok := <-e.ch:
			if !ok {
				return
			}
			e.xadd(evt)
		case <-e.stop:
			// 优雅关闭：排空 channel 中剩余事件再退出
			e.drain()
			return
		}
	}
}

// drain 非阻塞排空 channel 中剩余事件。
func (e *Emitter) drain() {
	for {
		select {
		case evt := <-e.ch:
			e.xadd(evt)
		default:
			return
		}
	}
}

// xadd 把单条事件 XADD 到 Redis Stream。失败只 warn，不重试（避免无限堆积）。
func (e *Emitter) xadd(evt *domain.Event) {
	values, err := encodeEvent(evt)
	if err != nil {
		logger.L.Warn("event encode failed, dropping",
			zap.String("event_type", evt.Type),
			zap.String("event_id", evt.ID),
			zap.Error(err),
		)
		atomic.AddUint64(&e.droppedCount, 1)
		return
	}

	stream := e.streamPrefix + evt.Type
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	args := &redis.XAddArgs{
		Stream: stream,
		Values: values,
	}
	if e.maxLen > 0 {
		args.MaxLen = e.maxLen
		args.Approx = true
	}

	if err := e.client.XAdd(ctx, args).Err(); err != nil {
		logger.L.Warn("event XADD failed, dropping",
			zap.String("stream", stream),
			zap.String("event_id", evt.ID),
			zap.Error(err),
		)
		atomic.AddUint64(&e.droppedCount, 1)
		return
	}
}

// StreamName 返回某事件类型对应的 stream 名（供 Consumer 订阅时使用）。
func (e *Emitter) StreamName(eventType string) string {
	return fmt.Sprintf("%s%s", e.streamPrefix, eventType)
}
