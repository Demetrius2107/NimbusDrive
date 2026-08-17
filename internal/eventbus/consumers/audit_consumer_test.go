package consumers

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/Demetrius2107/NimbusDrive/internal/domain"
	"github.com/Demetrius2107/NimbusDrive/internal/logger"
)

// TestMain 初始化 logger，避免 noop/audit consumer handler 中 logger.L 为 nil panic。
func TestMain(m *testing.M) {
	if err := logger.Init("test", "error", ""); err != nil {
		// logger.Init 在 logDir 为空时可能失败，用 stderr 兜底
		os.Exit(m.Run())
	}
	os.Exit(m.Run())
}

func TestEventToOperationLog_FileUploaded(t *testing.T) {
	evt := &domain.Event{
		ID:         "evt-1",
		Type:       domain.EventFileUploaded,
		OccurredAt: time.Now().UTC(),
		ActorID:    42,
		Payload: map[string]any{
			"file_id":     float64(100),
			"hash_sha256": "abc",
			"size":        float64(1024),
		},
	}
	log := eventToOperationLog(evt)

	if log.Action != "event."+domain.EventFileUploaded {
		t.Errorf("Action: got %q want %q", log.Action, "event."+domain.EventFileUploaded)
	}
	if log.ActorType != "user" {
		t.Errorf("ActorType: got %q want user", log.ActorType)
	}
	if log.ActorID == nil || *log.ActorID != 42 {
		t.Errorf("ActorID: got %v want 42", log.ActorID)
	}
	if log.TargetType == nil || *log.TargetType != "file" {
		t.Errorf("TargetType: got %v want file", log.TargetType)
	}
	if log.TargetID == nil || *log.TargetID != "100" {
		t.Errorf("TargetID: got %v want 100", log.TargetID)
	}
	// detail 应为合法 JSON
	var detail map[string]any
	if err := json.Unmarshal(log.Detail, &detail); err != nil {
		t.Fatalf("detail not valid json: %v", err)
	}
	if detail["hash_sha256"] != "abc" {
		t.Errorf("detail hash: got %v want abc", detail["hash_sha256"])
	}
}

func TestEventToOperationLog_ShareCreated(t *testing.T) {
	evt := &domain.Event{
		ID:         "evt-2",
		Type:       domain.EventShareCreated,
		OccurredAt: time.Now().UTC(),
		ActorID:    7,
		Payload: map[string]any{
			"share_id":     "SHAREABC123",
			"file_id":      float64(50),
			"has_password": true,
		},
	}
	log := eventToOperationLog(evt)

	if log.TargetType == nil || *log.TargetType != "share" {
		t.Errorf("TargetType: got %v want share", log.TargetType)
	}
	if log.TargetID == nil || *log.TargetID != "SHAREABC123" {
		t.Errorf("TargetID: got %v want SHAREABC123", log.TargetID)
	}
}

func TestEventToOperationLog_ShareAccessed_SystemActor(t *testing.T) {
	// share.accessed 公开端点无登录用户，ActorID=0 → ActorType=system
	evt := &domain.Event{
		ID:         "evt-3",
		Type:       domain.EventShareAccessed,
		OccurredAt: time.Now().UTC(),
		ActorID:    0,
		Payload: map[string]any{
			"share_id":    "SHAREXYZ",
			"file_id":     float64(50),
			"accessor_ip": "1.2.3.4",
		},
	}
	log := eventToOperationLog(evt)

	if log.ActorType != "system" {
		t.Errorf("ActorType: got %q want system", log.ActorType)
	}
	if log.ActorID != nil {
		t.Errorf("ActorID: got %v want nil (system actor)", log.ActorID)
	}
}

func TestEventToOperationLog_UserRegistered(t *testing.T) {
	evt := &domain.Event{
		ID:         "evt-4",
		Type:       domain.EventUserRegistered,
		OccurredAt: time.Now().UTC(),
		ActorID:    99,
		Payload: map[string]any{
			"user_id":  float64(99),
			"username": "alice",
			"email":    "alice@example.com",
		},
	}
	log := eventToOperationLog(evt)

	if log.TargetType == nil || *log.TargetType != "user" {
		t.Errorf("TargetType: got %v want user", log.TargetType)
	}
	if log.TargetID == nil || *log.TargetID != "99" {
		t.Errorf("TargetID: got %v want 99", log.TargetID)
	}
}

func TestEventToOperationLog_UnknownType(t *testing.T) {
	evt := &domain.Event{
		ID:         "evt-5",
		Type:       "unknown.event",
		OccurredAt: time.Now().UTC(),
		ActorID:    1,
		Payload:    map[string]any{},
	}
	log := eventToOperationLog(evt)
	if log.TargetType != nil {
		t.Errorf("TargetType: got %v want nil for unknown type", log.TargetType)
	}
	if log.TargetID != nil {
		t.Errorf("TargetID: got %v want nil for unknown type", log.TargetID)
	}
}

func TestAuditConsumer_Streams(t *testing.T) {
	a := NewAuditConsumer(nil)
	streams := a.Streams("nimbus:events:")
	if len(streams) != 4 {
		t.Fatalf("expected 4 streams, got %d", len(streams))
	}
	// 应包含全部 4 类事件
	want := map[string]bool{
		"nimbus:events:" + domain.EventFileUploaded:   false,
		"nimbus:events:" + domain.EventShareCreated:   false,
		"nimbus:events:" + domain.EventShareAccessed:  false,
		"nimbus:events:" + domain.EventUserRegistered: false,
	}
	for _, s := range streams {
		if _, ok := want[s]; ok {
			want[s] = true
		}
	}
	for s, found := range want {
		if !found {
			t.Errorf("missing stream %s", s)
		}
	}
}

func TestAuditConsumer_Handler_NilAggregator(t *testing.T) {
	// la 为 nil 时 handler 应 no-op 不 panic
	a := NewAuditConsumer(nil)
	h := a.Handler()
	err := h(nil, &domain.Event{
		Type:    domain.EventFileUploaded,
		ID:      "x",
		ActorID: 1,
		Payload: map[string]any{},
	})
	if err != nil {
		t.Fatalf("nil aggregator handler should not error, got %v", err)
	}
}

func TestStreamsConfigs(t *testing.T) {
	// 验证各消费者的 Streams 配置正确
	cases := []struct {
		name    string
		builder ConsumerBuilder
		wantLen int
		wantType string
	}{
		{"upload_stats", NewUploadStatsConsumer(nil), 1, domain.EventFileUploaded},
		{"quota_reconcile", NewQuotaReconcileConsumer(nil, nil), 1, domain.EventFileUploaded},
		{"noop", NewNoopConsumer(), 1, domain.EventFileUploaded},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			streams := tc.builder.Streams("nimbus:events:")
			if len(streams) != tc.wantLen {
				t.Fatalf("%s: expected %d streams, got %d", tc.name, tc.wantLen, len(streams))
			}
			want := "nimbus:events:" + tc.wantType
			if streams[0] != want {
				t.Errorf("%s: stream[0]=%q want %q", tc.name, streams[0], want)
			}
		})
	}
}

func TestNoopConsumer_Handler(t *testing.T) {
	n := NewNoopConsumer()
	h := n.Handler()
	err := h(nil, &domain.Event{
		Type:    domain.EventFileUploaded,
		ID:      "x",
		ActorID: 1,
		Payload: map[string]any{},
	})
	if err != nil {
		t.Fatalf("noop handler should not error, got %v", err)
	}
}

func TestUploadStatsConsumer_Handler_NilClient(t *testing.T) {
	u := NewUploadStatsConsumer(nil)
	h := u.Handler()
	err := h(nil, &domain.Event{
		Type:    domain.EventFileUploaded,
		ID:      "x",
		ActorID: 1,
		Payload: map[string]any{"size": float64(100)},
	})
	if err != nil {
		t.Fatalf("nil client handler should not error, got %v", err)
	}
}

func TestQuotaReconcileConsumer_Handler_NilDeps(t *testing.T) {
	q := NewQuotaReconcileConsumer(nil, nil)
	h := q.Handler()
	err := h(nil, &domain.Event{
		Type:    domain.EventFileUploaded,
		ID:      "x",
		ActorID: 1,
		Payload: map[string]any{},
	})
	if err != nil {
		t.Fatalf("nil deps handler should not error, got %v", err)
	}
}
