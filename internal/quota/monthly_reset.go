package quota

import (
	"context"
	"time"

	"github.com/Demetrius2107/NimbusDrive/internal/logger"
	"go.uber.org/zap"
)

// monthlyReseter 抽象批量创建当月 quota_periods 行的能力（QuotaRepo 实现）。
// 放在 quota 包以复用 currentPeriod 语义，但 SQL 在 store 包，故用接口解耦。
type monthlyReseter interface {
	ResetMonthlyAll(ctx context.Context, period string) (int64, error)
}

// MonthlyResetCron 月度配额重置定时任务。
//
// 设计：不依赖外部 cron 守护进程，进程内 time.Ticker 每 6 小时检查一次是否进入新月。
// 检测到 period 变化时，为所有活跃用户批量创建新月度 quota_periods 行（已存在则跳过）。
//
// 幂等：ResetMonthlyAll 用 ON CONFLICT DO NOTHING，多次触发同一 period 不会重复插入。
// 容错：进程重启后若已错过月初，下次 tick 会补建当月行（幂等）。
type MonthlyResetCron struct {
	reseter monthlyReseter
	period  string // 已处理的最近 period，避免同一周期重复执行
}

// NewMonthlyResetCron 构造。reseter 为 nil 时不启动（PG 不可用时降级）。
func NewMonthlyResetCron(reseter monthlyReseter) *MonthlyResetCron {
	return &MonthlyResetCron{reseter: reseter}
}

// checkInterval 检查间隔。6 小时足够覆盖月初任何时刻，又不至于频繁打 PG。
const checkInterval = 6 * time.Hour

// Start 启动后台 goroutine，返回停止函数。
// ctx 取消时停止。reseter 为 nil 直接返回空停止函数（降级）。
func (c *MonthlyResetCron) Start(ctx context.Context) func() {
	if c.reseter == nil {
		return func() {}
	}
	stop := make(chan struct{})
	go func() {
		defer close(stop)
		// 启动时立即检查一次（覆盖进程重启后跨月的场景）。
		c.tick(ctx)
		ticker := time.NewTicker(checkInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				c.tick(ctx)
			}
		}
	}()
	return func() { <-stop }
}

// tick 执行一次检查：若当月 period 与上次不同，批量建当月行。
func (c *MonthlyResetCron) tick(ctx context.Context) {
	period := currentPeriod()
	if period == c.period {
		return // 本周期已处理
	}
	affected, err := c.reseter.ResetMonthlyAll(ctx, period)
	if err != nil {
		logger.L.Error("monthly quota reset failed",
			zap.String("period", period), zap.Error(err))
		return
	}
	c.period = period
	logger.L.Info("monthly quota reset done",
		zap.String("period", period), zap.Int64("affected", affected))
}

// currentPeriod 返回当前 UTC 月份 "YYYY-MM"。
// 与 store.currentPeriod 同义，cron 侧独立定义避免跨包循环依赖。
func currentPeriod() string {
	return time.Now().UTC().Format("2006-01")
}
