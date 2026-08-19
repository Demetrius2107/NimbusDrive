package eventbus

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Demetrius2107/NimbusDrive/internal/domain"
	"github.com/Demetrius2107/NimbusDrive/internal/logger"
	"github.com/Demetrius2107/NimbusDrive/internal/tracing"
	"github.com/go-redis/redis/v8"
	"go.uber.org/zap"
	"go.opentelemetry.io/otel/trace"
)

// Handler 处理一条事件。返回 nil 表示处理成功（消息将被 ACK）；
// 返回 error 表示处理失败（按重试策略处理，超阈值进 DLQ）。
// Handler 必须幂等：同一事件可能因重投递被处理多次。
type Handler func(ctx context.Context, evt *domain.Event) error

// Consumer 是事件总线消费者。按消费者组订阅一条或多条 stream，
// 保证至少一次投递（成功才 ACK）+ 毒丸隔离（超阈值进 DLQ）+ 优雅关闭。
type Consumer struct {
	client    *redis.Client
	group     string
	name      string
	streams   []string
	handler   Handler
	maxRetries int
	blockMs   int
	dlqPrefix string

	// 幂等去重键 TTL。已处理事件在 TTL 内重复投递会被跳过。
	idempotencyTTL time.Duration

	// 原子计数器：供 metrics.InfraCollector scrape。无锁读，热路径零开销。
	processedCount uint64 // 成功处理（ACK）的消息数
	errorCount     uint64 // 处理失败（handler 返回 err）的消息数
	dlqCount       uint64 // 移入死信队列的消息数

	wg   sync.WaitGroup
	stop chan struct{}
}

// NewConsumer 构造消费者（未启动）。client 为 nil 时 Start 为 no-op。
func NewConsumer(
	client *redis.Client,
	group, name string,
	streams []string,
	handler Handler,
	maxRetries, blockMs int,
	dlqPrefix string,
) *Consumer {
	return &Consumer{
		client:         client,
		group:          group,
		name:           name,
		streams:        streams,
		handler:        handler,
		maxRetries:     maxRetries,
		blockMs:        blockMs,
		dlqPrefix:      dlqPrefix,
		idempotencyTTL: 24 * time.Hour,
		stop:           make(chan struct{}),
	}
}

// Start 创建消费者组（若不存在）并启动后台消费 goroutine。
// 每条 stream 一个 goroutine，独立阻塞 XREADGROUP。
func (c *Consumer) Start() error {
	if c == nil || c.client == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, stream := range c.streams {
		// MkStream：stream 不存在时自动创建
		if err := c.client.XGroupCreateMkStream(ctx, stream, c.group, "$").Err(); err != nil {
			// BUSYGROUP 表示组已存在，非错误
			if !isBusyGroupErr(err) {
				return fmt.Errorf("create consumer group for %s: %w", stream, err)
			}
		}
		c.wg.Add(1)
		go c.run(stream)
	}
	return nil
}

// Close 优雅关闭：停止拉取新消息 → 等待处理中的消息完成 → 退出。
// 必须在 Emitter.Close() 之前调用（消费者先停，生产者后停）。
func (c *Consumer) Close() {
	if c == nil || c.client == nil {
		return
	}
	close(c.stop)
	c.wg.Wait()
}

// run 单条 stream 的消费循环。
func (c *Consumer) run(stream string) {
	defer c.wg.Done()
	logger.L.Info("consumer started",
		zap.String("stream", stream),
		zap.String("group", c.group),
		zap.String("consumer", c.name),
	)

	// 启动时先接管超时未 ACK 的消息（消费者重启恢复）
	c.reclaimStale(context.Background(), stream)

	for {
		select {
		case <-c.stop:
			logger.L.Info("consumer stopping", zap.String("stream", stream))
			return
		default:
		}
		c.consumeBatch(context.Background(), stream)
	}
}

// consumeBatch 拉取并处理一批消息。
func (c *Consumer) consumeBatch(ctx context.Context, stream string) {
	readCtx, cancel := context.WithTimeout(ctx, time.Duration(c.blockMs+1000)*time.Millisecond)
	defer cancel()

	streamsIdx := []string{stream, ">"}
	res, err := c.client.XReadGroup(readCtx, &redis.XReadGroupArgs{
		Group:    c.group,
		Consumer: c.name,
		Streams:  streamsIdx,
		Count:    10,
		Block:    time.Duration(c.blockMs) * time.Millisecond,
		NoAck:    false,
	}).Result()

	if err != nil {
		// redis.Nil 表示 block 超时无消息，正常
		if err == redis.Nil {
			return
		}
		// context 超时/取消也视为正常轮询
		if isContextErr(err) {
			return
		}
		logger.L.Warn("XREADGROUP failed",
			zap.String("stream", stream),
			zap.Error(err),
		)
		time.Sleep(time.Second) // 退避，避免错误时空转打满 CPU
		return
	}

	for _, xs := range res {
		for _, msg := range xs.Messages {
			c.handleMessage(ctx, stream, msg)
		}
	}
}

// handleMessage 处理单条消息：解码 → 幂等检查 → handler → ACK/重试/DLQ。
func (c *Consumer) handleMessage(ctx context.Context, stream string, msg redis.XMessage) {
	evt, err := decodeEvent(msg.Values)
	if err != nil {
		logger.L.Warn("event decode failed, moving to DLQ",
			zap.String("stream", stream),
			zap.String("msg_id", msg.ID),
			zap.Error(err),
		)
		c.moveToDLQ(ctx, stream, msg, err)
		c.ack(ctx, stream, msg.ID)
		return
	}

	// 幂等去重：已处理过的事件跳过
	if c.isProcessed(ctx, evt.ID) {
		c.ack(ctx, stream, msg.ID)
		return
	}

	// 查询 delivery count（已投递次数）判断是否毒丸
	deliveryCount, err := c.getDeliveryCount(ctx, stream, msg.ID)
	if err != nil {
		logger.L.Debug("query delivery count failed, proceed anyway",
			zap.String("msg_id", msg.ID),
			zap.Error(err),
		)
		deliveryCount = 1
	}

	// 提取 trace context 并起 consumer span，续接生产者 trace。
	// evt.TraceContext 由 Emitter 从请求 ctx 注入；无则起根 span。
	ctx = tracing.Extract(ctx, evt.TraceContext)
	tracer := tracing.Tracer("eventbus.consumer")
	ctx, span := tracer.Start(ctx, "consume."+evt.Type,
		trace.WithSpanKind(trace.SpanKindConsumer),
	)
	defer span.End()

	if err := c.handler(ctx, evt); err != nil {
		atomic.AddUint64(&c.errorCount, 1)
		if deliveryCount >= c.maxRetries {
			// 超阈值：毒丸消息移入 DLQ，ACK 原消息释放 PEL
			logger.L.Warn("event handler exceeded max retries, moving to DLQ",
				zap.String("event_type", evt.Type),
				zap.String("event_id", evt.ID),
				zap.Int("delivery_count", deliveryCount),
				zap.Int("max_retries", c.maxRetries),
				zap.Error(err),
			)
			c.moveToDLQ(ctx, stream, msg, err)
			c.ack(ctx, stream, msg.ID)
			return
		}
		// 未超阈值：不 ACK，留在 PEL 等待重投递（XAUTOCLAIM 或下次 XREADGROUP 的 0 起点读取）
		logger.L.Warn("event handler failed, will retry",
			zap.String("event_type", evt.Type),
			zap.String("event_id", evt.ID),
			zap.Int("delivery_count", deliveryCount),
			zap.Error(err),
		)
		return
	}

	// 成功：标记已处理 + ACK
	c.markProcessed(ctx, evt.ID)
	c.ack(ctx, stream, msg.ID)
	atomic.AddUint64(&c.processedCount, 1)
}

// isProcessed 幂等检查：事件 ID 是否已被本消费者处理过。
// 键 nimbus:processed:{consumer}:{eventID}，SETNX。
func (c *Consumer) isProcessed(ctx context.Context, eventID string) bool {
	key := c.processedKey(eventID)
	ok, err := c.client.SetNX(ctx, key, "1", c.idempotencyTTL).Result()
	if err != nil {
		// Redis 出错时不能假设已处理（可能漏处理），返回 false 让 handler 处理
		logger.L.Debug("idempotency check failed, proceed",
			zap.String("event_id", eventID),
			zap.Error(err),
		)
		return false
	}
	return !ok // SetNX 返回 false 表示键已存在（已处理）
}

// markProcessed 标记事件已处理（handler 成功后调用，确保幂等键存在）。
func (c *Consumer) markProcessed(ctx context.Context, eventID string) {
	key := c.processedKey(eventID)
	_ = c.client.Set(ctx, key, "1", c.idempotencyTTL).Err()
}

func (c *Consumer) processedKey(eventID string) string {
	return fmt.Sprintf("nimbus:processed:%s:%s", c.name, eventID)
}

// getDeliveryCount 查询消息的投递次数（XPendingExt）。
func (c *Consumer) getDeliveryCount(ctx context.Context, stream, msgID string) (int, error) {
	res, err := c.client.XPendingExt(ctx, &redis.XPendingExtArgs{
		Stream:   stream,
		Group:    c.group,
		Start:    msgID,
		End:      msgID,
		Count:    1,
	}).Result()
	if err != nil || len(res) == 0 {
		return 0, err
	}
	return int(res[0].RetryCount), nil
}

// moveToDLQ 把消息移入死信队列 stream，保留原消息 ID 与错误信息。
func (c *Consumer) moveToDLQ(ctx context.Context, stream string, msg redis.XMessage, handlerErr error) {
	dlqStream := c.dlqPrefix + stream
	values := make(map[string]interface{}, len(msg.Values)+2)
	for k, v := range msg.Values {
		values[k] = v
	}
	values["__dlq_origin_stream"] = stream
	values["__dlq_origin_id"] = msg.ID
	values["__dlq_error"] = handlerErr.Error()
	values["__dlq_moved_at"] = time.Now().UTC().Format(time.RFC3339Nano)

	if err := c.client.XAdd(ctx, &redis.XAddArgs{
		Stream: dlqStream,
		Values: values,
	}).Err(); err != nil {
		logger.L.Error("move to DLQ failed",
			zap.String("dlq_stream", dlqStream),
			zap.String("origin_id", msg.ID),
			zap.Error(err),
		)
		return
	}
	atomic.AddUint64(&c.dlqCount, 1)
}

// PendingLength 返回某 stream 的消费者组 PEL（pending entries list）长度——
// 即已投递未 ACK 的消息数，consumer lag 的直接度量。
// 由 metrics.InfraCollector 在 /metrics 抓取时调用（scrape-time，非后台轮询）。
// Redis 不可用或 group 不存在时返回 error，collector 侧降级为 0。
func (c *Consumer) PendingLength(stream string) (int64, error) {
	if c == nil || c.client == nil {
		return 0, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	res, err := c.client.XPending(ctx, stream, c.group).Result()
	if err != nil {
		return 0, err
	}
	return res.Count, nil
}

// Streams 返回该 consumer 订阅的所有 stream（供 collector 遍历 scrape pending）。
func (c *Consumer) Streams() []string {
	if c == nil {
		return nil
	}
	out := make([]string, len(c.streams))
	copy(out, c.streams)
	return out
}

// ProcessedCount 返回成功处理（ACK）的消息总数。
func (c *Consumer) ProcessedCount() uint64 {
	if c == nil {
		return 0
	}
	return atomic.LoadUint64(&c.processedCount)
}

// ErrorCount 返回处理失败（handler 返回 err）的消息总数。
func (c *Consumer) ErrorCount() uint64 {
	if c == nil {
		return 0
	}
	return atomic.LoadUint64(&c.errorCount)
}

// DLQCount 返回移入死信队列的消息总数。
func (c *Consumer) DLQCount() uint64 {
	if c == nil {
		return 0
	}
	return atomic.LoadUint64(&c.dlqCount)
}

// ack 确认消息已处理。失败只 warn，下次重投递会重新处理（幂等保证安全）。
func (c *Consumer) ack(ctx context.Context, stream, msgID string) {
	if err := c.client.XAck(ctx, stream, c.group, msgID).Err(); err != nil {
		logger.L.Warn("XACK failed",
			zap.String("stream", stream),
			zap.String("msg_id", msgID),
			zap.Error(err),
		)
	}
}

// reclaimStale 接管超时未 ACK 的消息（消费者重启恢复场景）。
// 用 XAUTOCLAIM 把 idle 超过 minIdleTime 的消息重新分配给本消费者。
func (c *Consumer) reclaimStale(ctx context.Context, stream string) {
	minIdle := 30 * time.Second
	start := "0"
	for {
		msgs, next, err := c.client.XAutoClaim(ctx, &redis.XAutoClaimArgs{
			Stream:   stream,
			Group:    c.group,
			Consumer: c.name,
			MinIdle:  minIdle,
			Start:    start,
			Count:    100,
		}).Result()
		if err != nil {
			if err == redis.Nil {
				return
			}
			logger.L.Debug("XAUTOCLAIM failed",
				zap.String("stream", stream),
				zap.Error(err),
			)
			return
		}
		for _, msg := range msgs {
			c.handleMessage(ctx, stream, msg)
		}
		if next == "0" || next == start {
			return
		}
		start = next
	}
}

func isBusyGroupErr(err error) bool {
	return err != nil && strings.Contains(err.Error(), "BUSYGROUP")
}

func isContextErr(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "context deadline exceeded") || strings.Contains(s, "context canceled")
}
