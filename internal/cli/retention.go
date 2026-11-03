package cli

import (
	"context"
	"log/slog"
	"time"

	"github.com/crazychengmm/cagent/internal/config"
	"github.com/crazychengmm/cagent/internal/server/store"
)

// startRetentionJanitor 启动后台数据清理：按配置保留期删除终态 job/输出与审计。
// 保留期为负值表示禁用对应清理；随 ctx 取消退出。
func startRetentionJanitor(ctx context.Context, db *store.Store, cfg *config.ServerConfig, logger *slog.Logger) {
	interval := cfg.PurgeInterval.Std()
	if interval <= 0 {
		interval = time.Hour
	}
	jobRetention := cfg.JobRetention.Std()
	auditRetention := cfg.AuditRetention.Std()
	if jobRetention <= 0 && auditRetention <= 0 {
		return // 两类清理均禁用
	}

	run := func() {
		now := time.Now().UTC()
		var jobBefore, auditBefore time.Time
		if jobRetention > 0 {
			jobBefore = now.Add(-jobRetention)
		}
		if auditRetention > 0 {
			auditBefore = now.Add(-auditRetention)
		}
		jobs, audits, err := db.PurgeOldData(ctx, jobBefore, auditBefore)
		if err != nil {
			logger.Error("数据清理失败", "error", err.Error())
			return
		}
		if jobs > 0 || audits > 0 {
			logger.Info("数据清理完成", "jobs", jobs, "audits", audits)
		}
	}

	go func() {
		// 启动后稍等，避免与启动流程争抢；之后按周期清理。
		timer := time.NewTimer(30 * time.Second)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
				run()
				timer.Reset(interval)
			}
		}
	}()
}
