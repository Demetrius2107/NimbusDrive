package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
)

// InfraSources 抽象 infra 指标数据源，供 InfraCollector 在 scrape 时查询。
// 用接口而非直接依赖 *eventbus.Emitter 等具体类型——避免 metrics 包反向依赖
// eventbus/adminstore（保持依赖方向：infra → metrics，非 metrics → infra）。
// main 构造 InfraCollector 时注入实现，nil 项跳过对应指标。
type InfraSources struct {
	Emitter  EmitterMetrics    // 可空
	Consumer ConsumerMetrics   // 可空
	LogAgg   LogAggregatorMetrics // 可空
}

// EmitterMetrics 事件总线生产者指标。由 *eventbus.Emitter 实现。
type EmitterMetrics interface {
	EmittedCount() uint64 // 成功 XADD 的事件数
	DroppedCount() uint64 // 因背压/编码/XADD 失败丢弃的事件数
}

// ConsumerMetrics 事件总线消费者指标。由 *eventbus.Consumer 实现。
type ConsumerMetrics interface {
	// PendingLength 返回某 stream 的消费者组 PEL（pending entries list）长度。
	// 即已投递未 ACK 的消息数——consumer lag 的直接度量。
	PendingLength(stream string) (int64, error)
	// Streams 返回该 consumer 订阅的所有 stream（供 collector 遍历 scrape）。
	Streams() []string
	ProcessedCount() uint64 // 成功处理（ACK）的消息数
	ErrorCount() uint64     // 处理失败（handler 返回 err）的消息数
	DLQCount() uint64       // 移入死信队列的消息数
}

// LogAggregatorMetrics 管理日志聚合器指标。由 *adminstore.LogAggregator 实现。
type LogAggregatorMetrics interface {
	DroppedCount() uint64 // 因背压/DB 写失败丢弃的日志条数
}

// InfraCollector 是 prometheus.Collector 实现：在 /metrics 被抓取时现查
// InfraSources，emit infra 指标。不在后台 goroutine 轮询——这类派生指标
// 变化不频繁且查询有成本（XPENDING 是 Redis 往返），拉模型下 scrape-on-demand
// 是标准范式。nil 的 source 跳过对应指标族。
type InfraCollector struct {
	src InfraSources

	emitterEmitted  *prometheus.Desc
	emitterDropped  *prometheus.Desc
	consumerPending *prometheus.Desc
	consumerDLQ     *prometheus.Desc
	consumerProc    *prometheus.Desc
	consumerErr     *prometheus.Desc
	logDropped      *prometheus.Desc
}

// RegisterInfra 在 Collector 的 Registry 上注册一个 InfraCollector。
// main 在构造完 emitter/consumer/logAggregator 后调用。重复注册会 panic
// （MustRegister），表明 main 接线错误。各 src 字段可空。
func (c *Collector) RegisterInfra(src InfraSources) {
	if c == nil || c.Registry == nil {
		return
	}
	c.Registry.MustRegister(NewInfraCollector(src))
}

// NewInfraCollector 构造。src 各字段可空（nil 接口跳过对应指标）。
func NewInfraCollector(src InfraSources) *InfraCollector {
	return &InfraCollector{
		src: src,
		emitterEmitted:  prometheus.NewDesc("eventbus_emitter_emitted_total", "Events successfully XADD'd to Redis Streams.", nil, nil),
		emitterDropped:  prometheus.NewDesc("eventbus_emitter_dropped_total", "Events dropped due to backpressure/encode/XADD failure.", nil, nil),
		consumerPending: prometheus.NewDesc("eventbus_consumer_pending_messages", "Pending (delivered but unacked) messages per stream — consumer lag.", []string{"stream"}, nil),
		consumerDLQ:     prometheus.NewDesc("eventbus_consumer_dlq_total", "Messages moved to dead-letter queue.", nil, nil),
		consumerProc:    prometheus.NewDesc("eventbus_consumer_messages_processed_total", "Messages successfully processed and acked.", nil, nil),
		consumerErr:     prometheus.NewDesc("eventbus_consumer_message_processing_errors_total", "Message processing errors (handler returned err).", nil, nil),
		logDropped:      prometheus.NewDesc("admin_log_dropped_total", "Admin log entries dropped due to backpressure/DB write failure.", nil, nil),
	}
}

// Describe 实现 prometheus.Collector。发送所有 desc；nil source 的 desc 也发
// （抓取时 Collect 会跳过）。
func (ic *InfraCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- ic.emitterEmitted
	ch <- ic.emitterDropped
	ch <- ic.consumerPending
	ch <- ic.consumerDLQ
	ch <- ic.consumerProc
	ch <- ic.consumerErr
	ch <- ic.logDropped
}

// Collect 实现 prometheus.Collector。scrape 时现查各 source，emit 指标。
// nil source 跳过。XPENDING 错误不 abort 整次采集——emit 0 并继续其他指标。
func (ic *InfraCollector) Collect(ch chan<- prometheus.Metric) {
	if ic.src.Emitter != nil {
		ch <- prometheus.MustNewConstMetric(ic.emitterEmitted, prometheus.CounterValue, float64(ic.src.Emitter.EmittedCount()))
		ch <- prometheus.MustNewConstMetric(ic.emitterDropped, prometheus.CounterValue, float64(ic.src.Emitter.DroppedCount()))
	}
	if ic.src.Consumer != nil {
		for _, s := range ic.src.Consumer.Streams() {
			n, err := ic.src.Consumer.PendingLength(s)
			if err != nil {
				// Redis 不可用或 group 不存在：emit 0，不阻塞其他指标
				n = 0
			}
			ch <- prometheus.MustNewConstMetric(ic.consumerPending, prometheus.GaugeValue, float64(n), s)
		}
		ch <- prometheus.MustNewConstMetric(ic.consumerDLQ, prometheus.CounterValue, float64(ic.src.Consumer.DLQCount()))
		ch <- prometheus.MustNewConstMetric(ic.consumerProc, prometheus.CounterValue, float64(ic.src.Consumer.ProcessedCount()))
		ch <- prometheus.MustNewConstMetric(ic.consumerErr, prometheus.CounterValue, float64(ic.src.Consumer.ErrorCount()))
	}
	if ic.src.LogAgg != nil {
		ch <- prometheus.MustNewConstMetric(ic.logDropped, prometheus.CounterValue, float64(ic.src.LogAgg.DroppedCount()))
	}
}
