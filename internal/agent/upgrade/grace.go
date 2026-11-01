package upgrade

import (
	"context"
	"errors"
	"time"
)

// ErrJobsRunning 表示 grace 窗口结束后仍有任务未完成。
var ErrJobsRunning = errors.New("存在运行中任务，grace 窗口内未结束，放弃本次升级")

// ActiveTracker 提供当前活跃（排队/在跑）任务数。
type ActiveTracker interface {
	ActiveCount() int
}

// gracePollInterval 是空闲检查周期。
const gracePollInterval = 100 * time.Millisecond

// WaitForIdle 立即检查并在 timeout 内等待任务清零；超时返回 ErrJobsRunning。
// 绝不取消用户任务。
func WaitForIdle(ctx context.Context, tracker ActiveTracker, timeout time.Duration) error {
	if tracker.ActiveCount() == 0 {
		return nil
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(gracePollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if tracker.ActiveCount() == 0 {
				return nil
			}
		case <-deadline.C:
			return ErrJobsRunning
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}
