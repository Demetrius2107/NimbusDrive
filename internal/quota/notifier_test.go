package quota

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// TestNotifier_NilClient_NoOp 验证 Redis 不可用时 Notifier 降级为 no-op。
// NotifyChange 不阻塞、不 panic，返回 0 版本号 + nil err。
// ReplaySince 返回空切片 + nil err。
func TestNotifier_NilClient_NoOp(t *testing.T) {
	n := NewNotifier(nil)

	v, err := n.NotifyChange(context.Background(), 0, ChangeResetAll, map[string]any{"quota": 100})
	if err != nil {
		t.Fatalf("nil client NotifyChange should not error, got %v", err)
	}
	if v != 0 {
		t.Errorf("nil client should return version 0, got %d", v)
	}

	evts, err := n.ReplaySince(context.Background(), 0, 1)
	if err != nil {
		t.Fatalf("nil client ReplaySince should not error, got %v", err)
	}
	if len(evts) != 0 {
		t.Errorf("nil client should return no events, got %d", len(evts))
	}
}

// TestChangeType_Values 验证变更类型常量值稳定（SSE 客户端按字符串匹配）。
func TestChangeType_Values(t *testing.T) {
	cases := map[ChangeType]string{
		ChangeResetAll:   "reset_all",
		ChangeUserQuota:  "user_quota_updated",
		ChangeUserStatus: "user_status_updated",
	}
	for ct, want := range cases {
		if string(ct) != want {
			t.Errorf("change type %q = %q, want %q", ct, string(ct), want)
		}
	}
}

// TestQuotaChangeEvent_TraceParent_JSON 验证 TraceParent 字段 JSON 序列化/反序列化往返，
// 以及 omitempty（空串时不输出字段）。
func TestQuotaChangeEvent_TraceParent_JSON(t *testing.T) {
	// 有 traceparent
	evt := QuotaChangeEvent{
		Version:     1,
		Type:        ChangeResetAll,
		TraceParent: "00-abc-def-01",
	}
	data, err := json.Marshal(evt)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(data), "trace_parent") {
		t.Errorf("trace_parent should be present in JSON: %s", data)
	}
	var decoded QuotaChangeEvent
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.TraceParent != evt.TraceParent {
		t.Errorf("TraceParent: got %q want %q", decoded.TraceParent, evt.TraceParent)
	}

	// 空 traceparent → omitempty
	evt2 := QuotaChangeEvent{Version: 2, Type: ChangeUserQuota}
	data2, _ := json.Marshal(evt2)
	if strings.Contains(string(data2), "trace_parent") {
		t.Errorf("trace_parent should be omitted when empty: %s", data2)
	}
}
