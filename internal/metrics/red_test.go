package metrics

import (
	"context"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

// TestExemplarFromContext_NoSpan 验证无 active span 时返回 nil（降级为普通 Observe）。
func TestExemplarFromContext_NoSpan(t *testing.T) {
	// 普通 ctx 无 span context
	ex := exemplarFromContext(context.Background())
	if ex != nil {
		t.Errorf("exemplarFromContext with no span should be nil, got %v", ex)
	}
}

// TestInfraCollector_EmitterOnly 验证只注入 emitter 时只 emit emitter 指标。
func TestInfraCollector_EmitterOnly(t *testing.T) {
	fe := &fakeEmitter{emitted: 42, dropped: 3}
	ic := NewInfraCollector(InfraSources{Emitter: fe})

	ch := make(chan prometheus.Metric, 10)
	ic.Collect(ch)
	close(ch)

	count := 0
	for range ch {
		count++
	}
	// emitter 产生 2 个指标（emitted + dropped）
	if count != 2 {
		t.Errorf("expected 2 emitter metrics, got %d", count)
	}
}

// fakeEmitter 实现 EmitterMetrics 接口供测试。
type fakeEmitter struct {
	emitted uint64
	dropped uint64
}

func (f *fakeEmitter) EmittedCount() uint64 { return f.emitted }
func (f *fakeEmitter) DroppedCount() uint64 { return f.dropped }

// TestInfraCollector_NilSources 验证全部 source 为 nil 时不 panic、不 emit。
func TestInfraCollector_NilSources(t *testing.T) {
	ic := NewInfraCollector(InfraSources{})
	ch := make(chan prometheus.Metric, 10)
	ic.Collect(ch)
	close(ch)

	count := 0
	for range ch {
		count++
	}
	if count != 0 {
		t.Errorf("nil sources should emit 0 metrics, got %d", count)
	}
}

// TestMultiConsumer_Aggregation 验证多 consumer 聚合：计数求和、stream 去重。
func TestMultiConsumer_Aggregation(t *testing.T) {
	c1 := &fakeConsumer{processed: 10, errors: 1, dlq: 0, streams: []string{"s1", "s2"}}
	c2 := &fakeConsumer{processed: 20, errors: 2, dlq: 5, streams: []string{"s2", "s3"}}

	mc := NewMultiConsumer(c1, c2)

	if got := mc.ProcessedCount(); got != 30 {
		t.Errorf("processed sum = %v, want 30", got)
	}
	if got := mc.ErrorCount(); got != 3 {
		t.Errorf("error sum = %v, want 3", got)
	}
	if got := mc.DLQCount(); got != 5 {
		t.Errorf("dlq sum = %v, want 5", got)
	}

	streams := mc.Streams()
	if len(streams) != 3 {
		t.Errorf("deduplicated streams count = %v, want 3 (s1,s2,s3)", len(streams))
	}
}

// fakeConsumer 实现 ConsumerMetrics 接口供测试。
type fakeConsumer struct {
	processed uint64
	errors    uint64
	dlq       uint64
	streams   []string
}

func (f *fakeConsumer) PendingLength(stream string) (int64, error) { return 0, nil }
func (f *fakeConsumer) Streams() []string                          { return f.streams }
func (f *fakeConsumer) ProcessedCount() uint64                     { return f.processed }
func (f *fakeConsumer) ErrorCount() uint64                         { return f.errors }
func (f *fakeConsumer) DLQCount() uint64                           { return f.dlq }
