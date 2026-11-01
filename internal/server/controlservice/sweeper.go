package controlservice

import (
	"context"
	"log/slog"
	"time"

	"github.com/crazychengmm/cagent/internal/server/store"
)

const defaultSweepInterval = 10 * time.Second

// RunOfflineSweeper 周期性把心跳超时的 online agent 标记为 offline，
// 阻塞直到 ctx 取消。interval<=0 时取默认 10s。
func RunOfflineSweeper(ctx context.Context, st *store.Store,
	heartbeatTimeout, interval time.Duration, logger *slog.Logger) {
	if interval <= 0 {
		interval = defaultSweepInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			sweepOnce(ctx, st, heartbeatTimeout, logger)
		case <-ctx.Done():
			return
		}
	}
}

// sweepOnce 执行一轮扫描，导出以便单测直接驱动。
func sweepOnce(ctx context.Context, st *store.Store,
	heartbeatTimeout time.Duration, logger *slog.Logger) {
	cutoff := time.Now().Add(-heartbeatTimeout)
	stale, err := st.ListAgentsOlderThan(ctx, cutoff)
	if err != nil {
		logger.Error("扫描超时 agent 失败", "error", err.Error())
		return
	}
	for _, agent := range stale {
		if err := st.SetAgentStatus(ctx, agent.AgentID, store.StatusOffline, time.Now().UTC()); err != nil {
			logger.Error("标记 offline 失败", "agent_id", agent.AgentID, "error", err.Error())
			continue
		}
		logger.Warn("心跳超时，agent 标记为 offline",
			"agent_id", agent.AgentID, "last_heartbeat", agent.LastHeartbeatAt)
	}
}
