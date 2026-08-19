package eventbus

import (
	"testing"
	"time"

	"github.com/Demetrius2107/NimbusDrive/internal/domain"
)

func TestEncodeDecode_RoundTrip(t *testing.T) {
	orig := &domain.Event{
		ID:         "evt-123",
		Type:       domain.EventFileUploaded,
		OccurredAt: time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC),
		ActorID:    42,
		Payload: map[string]any{
			"file_id":     float64(100),
			"hash_sha256": "abc123",
			"size":        float64(1024),
			"instant":     false,
			"nested":      map[string]any{"k": "v"},
		},
	}

	values, err := encodeEvent(orig)
	if err != nil {
		t.Fatalf("encodeEvent: %v", err)
	}

	// 验证编码后的字段都是字符串
	for k, v := range values {
		if _, ok := v.(string); !ok {
			t.Fatalf("field %s value not string: %T", k, v)
		}
	}

	decoded, err := decodeEvent(values)
	if err != nil {
		t.Fatalf("decodeEvent: %v", err)
	}

	if decoded.ID != orig.ID {
		t.Errorf("ID: got %q want %q", decoded.ID, orig.ID)
	}
	if decoded.Type != orig.Type {
		t.Errorf("Type: got %q want %q", decoded.Type, orig.Type)
	}
	if !decoded.OccurredAt.Equal(orig.OccurredAt) {
		t.Errorf("OccurredAt: got %v want %v", decoded.OccurredAt, orig.OccurredAt)
	}
	if decoded.ActorID != orig.ActorID {
		t.Errorf("ActorID: got %d want %d", decoded.ActorID, orig.ActorID)
	}
	// payload 经 JSON 往返后数字变 float64
	if got, want := decoded.Payload["hash_sha256"], orig.Payload["hash_sha256"]; got != want {
		t.Errorf("payload hash: got %v want %v", got, want)
	}
	if got, want := decoded.Payload["file_id"], orig.Payload["file_id"]; got != want {
		t.Errorf("payload file_id: got %v want %v", got, want)
	}
}

func TestEncodeEvent_NilEvent(t *testing.T) {
	if _, err := encodeEvent(nil); err == nil {
		t.Fatal("expected error for nil event")
	}
}

func TestEncodeEvent_NilPayload(t *testing.T) {
	// nil payload 应可编码（marshal 为 null）
	evt := &domain.Event{
		ID:         "x",
		Type:       "test",
		OccurredAt: time.Now().UTC(),
		ActorID:    1,
		Payload:    nil,
	}
	values, err := encodeEvent(evt)
	if err != nil {
		t.Fatalf("encodeEvent nil payload: %v", err)
	}
	decoded, err := decodeEvent(values)
	if err != nil {
		t.Fatalf("decodeEvent nil payload: %v", err)
	}
	if decoded.Payload != nil {
		t.Errorf("expected nil payload, got %v", decoded.Payload)
	}
}

func TestDecodeEvent_InvalidOccurredAt(t *testing.T) {
	values := map[string]interface{}{
		streamFieldID:         "x",
		streamFieldType:       "test",
		streamFieldOccurredAt: "not-a-date",
		streamFieldActorID:    "1",
		streamFieldPayload:    "{}",
	}
	if _, err := decodeEvent(values); err == nil {
		t.Fatal("expected error for invalid occurred_at")
	}
}

func TestDecodeEvent_InvalidActorID(t *testing.T) {
	values := map[string]interface{}{
		streamFieldID:         "x",
		streamFieldType:       "test",
		streamFieldOccurredAt: time.Now().UTC().Format(time.RFC3339Nano),
		streamFieldActorID:    "not-a-number",
		streamFieldPayload:    "{}",
	}
	if _, err := decodeEvent(values); err == nil {
		t.Fatal("expected error for invalid actor_id")
	}
}

func TestDecodeEvent_InvalidPayload(t *testing.T) {
	values := map[string]interface{}{
		streamFieldID:         "x",
		streamFieldType:       "test",
		streamFieldOccurredAt: time.Now().UTC().Format(time.RFC3339Nano),
		streamFieldActorID:    "1",
		streamFieldPayload:    "not json",
	}
	if _, err := decodeEvent(values); err == nil {
		t.Fatal("expected error for invalid payload json")
	}
}

// TestEncodeDecode_TraceContext 验证 trace_context 字段编解码往返。
func TestEncodeDecode_TraceContext(t *testing.T) {
	orig := &domain.Event{
		ID:         "evt-trace",
		Type:       domain.EventFileUploaded,
		OccurredAt: time.Now().UTC(),
		ActorID:    1,
		Payload:    map[string]any{"size": float64(100)},
		TraceContext: map[string]string{
			"traceparent": "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
			"tracestate":  "key=value",
		},
	}

	values, err := encodeEvent(orig)
	if err != nil {
		t.Fatalf("encodeEvent: %v", err)
	}
	// trace_context 字段应存在
	if _, ok := values[streamFieldTraceContext]; !ok {
		t.Fatal("trace_context field missing from encoded values")
	}

	decoded, err := decodeEvent(values)
	if err != nil {
		t.Fatalf("decodeEvent: %v", err)
	}
	if decoded.TraceContext == nil {
		t.Fatal("decoded TraceContext should not be nil")
	}
	if got, want := decoded.TraceContext["traceparent"], orig.TraceContext["traceparent"]; got != want {
		t.Errorf("traceparent: got %q want %q", got, want)
	}
	if got, want := decoded.TraceContext["tracestate"], orig.TraceContext["tracestate"]; got != want {
		t.Errorf("tracestate: got %q want %q", got, want)
	}
}

// TestEncodeDecode_NoTraceContext_BackwardCompat 验证无 trace_context 的旧消息可正常解码。
func TestEncodeDecode_NoTraceContext_BackwardCompat(t *testing.T) {
	orig := &domain.Event{
		ID:         "evt-old",
		Type:       domain.EventFileUploaded,
		OccurredAt: time.Now().UTC(),
		ActorID:    1,
		Payload:    map[string]any{"size": float64(100)},
		// TraceContext 为 nil（旧消息无此字段）
	}

	values, err := encodeEvent(orig)
	if err != nil {
		t.Fatalf("encodeEvent: %v", err)
	}
	// 不应写 trace_context 字段
	if _, ok := values[streamFieldTraceContext]; ok {
		t.Fatal("trace_context field should not be present for nil TraceContext")
	}

	decoded, err := decodeEvent(values)
	if err != nil {
		t.Fatalf("decodeEvent: %v", err)
	}
	if decoded.TraceContext != nil {
		t.Errorf("decoded TraceContext should be nil for old message, got %v", decoded.TraceContext)
	}
}
