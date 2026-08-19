package quota

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/Demetrius2107/NimbusDrive/internal/logger"
)

// TestMain 初始化 logger，避免 cron tick 中 logger.L 为 nil 时 panic。
func TestMain(m *testing.M) {
	_ = logger.Init("test", "error", "")
	os.Exit(m.Run())
}

// fakeReseter 记录 ResetMonthlyAll 调用，用于验证 cron 触发逻辑。
type fakeReseter struct {
	mu       sync.Mutex
	calls    []string
	failOnce bool
}

func (f *fakeReseter) ResetMonthlyAll(_ context.Context, period string) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failOnce {
		f.failOnce = false
		return 0, errors.New("simulated db error")
	}
	f.calls = append(f.calls, period)
	return int64(len(f.calls)), nil
}

func (f *fakeReseter) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// TestMonthlyResetCron_TickOncePerPeriod 验证同一 period 只重置一次（幂等）。
// 手动调用 tick 两次，第二次应跳过。
func TestMonthlyResetCron_TickOncePerPeriod(t *testing.T) {
	fr := &fakeReseter{}
	c := NewMonthlyResetCron(fr)

	c.tick(context.Background())
	c.tick(context.Background()) // 同 period，应跳过

	if got := fr.callCount(); got != 1 {
		t.Errorf("expected 1 reset call for same period, got %d", got)
	}
}

// TestMonthlyResetCron_NilReseter_NoOp 验证 nil reseter 降级。
func TestMonthlyResetCron_NilReseter_NoOp(t *testing.T) {
	c := NewMonthlyResetCron(nil)
	// Start 返回空停止函数，不 panic。
	stop := c.Start(context.Background())
	stop()
}

// TestMonthlyResetCron_TickErrorContinues 验证 ResetMonthlyAll 失败时不记录 period，
// 下次 tick 会重试（不把失败周期标记为已处理）。
func TestMonthlyResetCron_TickErrorContinues(t *testing.T) {
	fr := &fakeReseter{failOnce: true}
	c := NewMonthlyResetCron(fr)

	c.tick(context.Background()) // 失败，不标记 period
	if c.period != "" {
		t.Errorf("period should not be set on error, got %q", c.period)
	}
	c.tick(context.Background()) // 重试成功
	if c.period == "" {
		t.Error("period should be set after successful retry")
	}
	if got := fr.callCount(); got != 1 {
		t.Errorf("expected 1 successful call after retry, got %d", got)
	}
}

// TestMonthlyResetCron_StartStop 验证 Start 立即 tick 一次并可在 ctx 取消后停止。
func TestMonthlyResetCron_StartStop(t *testing.T) {
	fr := &fakeReseter{}
	c := NewMonthlyResetCron(fr)

	ctx, cancel := context.WithCancel(context.Background())
	stop := c.Start(ctx)
	// Start 内立即 tick 一次。给 goroutine 一点时间。
	time.Sleep(50 * time.Millisecond)
	cancel()
	stop() // 应返回

	if got := fr.callCount(); got != 1 {
		t.Errorf("expected immediate tick on Start, got %d calls", got)
	}
}
