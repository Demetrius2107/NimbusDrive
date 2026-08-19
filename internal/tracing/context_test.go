package tracing

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace"
)

// TestInit_NoneExporter_NoOp 验证 exporter=none 时不 panic，返回 no-op provider。
func TestInit_NoneExporter_NoOp(t *testing.T) {
	tp, err := Init("test", ExporterNone, "", 1.0)
	if err != nil {
		t.Fatalf("Init none should not error: %v", err)
	}
	if tp == nil {
		t.Fatal("Init none should return non-nil provider")
	}
	defer tp.Shutdown(context.Background())
}

// TestInit_StdoutExporter 验证 stdout exporter 可构造。
func TestInit_StdoutExporter(t *testing.T) {
	tp, err := Init("test", ExporterStdout, "", 1.0)
	if err != nil {
		t.Fatalf("Init stdout should not error: %v", err)
	}
	defer tp.Shutdown(context.Background())
}

// TestInit_UnknownExporter_Error 验证未知 exporter 返回错误。
func TestInit_UnknownExporter_Error(t *testing.T) {
	_, err := Init("test", "bogus", "", 1.0)
	if err == nil {
		t.Fatal("Init with unknown exporter should error")
	}
}

// TestTraceContextRoundTrip 验证 Inject → Extract 往返保真：
// 在一个 span 的 ctx 上 Inject 到 map，再从 map Extract 回新 ctx，
// 两个 ctx 的 SpanContext 的 TraceID 应一致。
func TestTraceContextRoundTrip(t *testing.T) {
	// 用 stdout exporter 初始化全局 propagator + tracer。
	tp, err := Init("test-roundtrip", ExporterStdout, "", 1.0)
	if err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	defer tp.Shutdown(context.Background())
	otel.SetTracerProvider(tp)

	tracer := Tracer("test")
	ctx, span := tracer.Start(context.Background(), "parent-span")
	defer span.End()

	// Inject 到 map
	tc := Inject(ctx)
	if tc == nil {
		t.Fatal("Inject should return non-nil map for active span")
	}
	// 应至少包含 traceparent
	if _, ok := tc["traceparent"]; !ok {
		t.Errorf("traceparent missing from injected map: %v", tc)
	}

	// Extract 到新 ctx
	ctx2 := Extract(context.Background(), tc)
	span2 := trace.SpanContextFromContext(ctx2)
	if !span2.IsValid() {
		t.Fatal("Extract should restore a valid SpanContext")
	}

	// TraceID 应一致
	origSC := trace.SpanContextFromContext(ctx)
	if span2.TraceID() != origSC.TraceID() {
		t.Errorf("TraceID mismatch: original=%s extracted=%s",
			origSC.TraceID(), span2.TraceID())
	}
}

// TestInject_NoSpan_Nil 验证无 active span 时 Inject 返回 nil。
func TestInject_NoSpan_Nil(t *testing.T) {
	tp, _ := Init("test-nospan", ExporterStdout, "", 1.0)
	defer tp.Shutdown(context.Background())
	otel.SetTracerProvider(tp)

	tc := Inject(context.Background())
	if tc != nil {
		t.Errorf("Inject with no active span should return nil, got %v", tc)
	}
}

// TestExtract_EmptyMap_NoOp 验证空 map 时 Extract 原样返回 ctx。
func TestExtract_EmptyMap_NoOp(t *testing.T) {
	tp, _ := Init("test-empty", ExporterStdout, "", 1.0)
	defer tp.Shutdown(context.Background())
	otel.SetTracerProvider(tp)

	ctx := Extract(context.Background(), nil)
	if ctx == nil {
		t.Fatal("Extract should not return nil ctx")
	}
	// 无 trace context，SpanContext 应无效
	if trace.SpanContextFromContext(ctx).IsValid() {
		t.Error("Extract from empty map should not produce valid SpanContext")
	}
}

// TestTraceParentString_RoundTrip 验证 TraceParentString → ExtractFromTraceParent 往返。
func TestTraceParentString_RoundTrip(t *testing.T) {
	tp, _ := Init("test-tp", ExporterStdout, "", 1.0)
	defer tp.Shutdown(context.Background())
	otel.SetTracerProvider(tp)

	tracer := Tracer("test")
	ctx, span := tracer.Start(context.Background(), "parent")
	defer span.End()

	tp2 := TraceParentString(ctx)
	if tp2 == "" {
		t.Fatal("TraceParentString should return non-empty for active span")
	}

	// 从 traceparent 文本还原
	ctx2 := ExtractFromTraceParent(context.Background(), tp2)
	sc := trace.SpanContextFromContext(ctx2)
	if !sc.IsValid() {
		t.Fatal("ExtractFromTraceParent should restore valid SpanContext")
	}

	origSC := trace.SpanContextFromContext(ctx)
	if sc.TraceID() != origSC.TraceID() {
		t.Errorf("TraceID mismatch after traceparent roundtrip: %s vs %s",
			sc.TraceID(), origSC.TraceID())
	}
}

// TestTraceParentString_NoSpan_Empty 验证无 span 时返回空串。
func TestTraceParentString_NoSpan_Empty(t *testing.T) {
	tp, _ := Init("test-tp-empty", ExporterStdout, "", 1.0)
	defer tp.Shutdown(context.Background())
	otel.SetTracerProvider(tp)

	if s := TraceParentString(context.Background()); s != "" {
		t.Errorf("TraceParentString with no span should return empty, got %q", s)
	}
}

// TestClampRatio 验证 ratio 钳制到 [0,1]。
func TestClampRatio(t *testing.T) {
	cases := []struct{ in, want float64 }{
		{-1, 0}, {0, 0}, {0.5, 0.5}, {1, 1}, {1.5, 1},
	}
	for _, c := range cases {
		if got := clampRatio(c.in); got != c.want {
			t.Errorf("clampRatio(%v) = %v, want %v", c.in, got, c.want)
		}
	}
}
