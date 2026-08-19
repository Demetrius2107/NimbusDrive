package quota

import (
	"context"
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
