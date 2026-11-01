package controlservice

import (
	"context"
	"log/slog"
	"time"

	"github.com/crazychengmm/cagent/internal/server/store"
)

// RunQueueExpirer 周期性把超过排队 TTL 的 pending job 置为 expired。
// 阻塞直到 ctx 取消；interval<=0 取默认扫描周期。
func RunQueueExpirer(ctx context.Context, st *store.Store,
	queueTTL, interval time.Duration, logger *slog.Logger) {
	if interval <= 0 {
		interval = defaultSweepInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			n, err := ExpireOnce(ctx, st, queueTTL, time.Now().UTC())
			if err != nil {
				logger.Error("过期任务扫描失败", "error", err.Error())
				continue
			}
			if n > 0 {
				logger.Warn("排队超时任务已置 expired", "count", n, "ttl", queueTTL.String())
			}
		case <-ctx.Done():
			return
		}
	}
}

// ExpireOnce 执行一轮过期，便于单测直接驱动。
func ExpireOnce(ctx context.Context, st *store.Store,
	queueTTL time.Duration, now time.Time) (int64, error) {
	cutoff := now.Add(-queueTTL)
	return st.ExpirePendingJobsBefore(ctx, cutoff)
}
