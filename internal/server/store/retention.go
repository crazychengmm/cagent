package store

import (
	"context"
	"time"
)

// ResetOnlineAgents 在 server 启动时调用：进程重启后所有控制流都已断开，
// DB 中残留的 online 是幽灵状态，统一置为 offline；agent 重连后的首次
// 心跳会立即把状态刷新回 online。返回被重置的 agent 数。
func (s *Store) ResetOnlineAgents(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE agents SET status = ?, updated_at = ? WHERE status = ?`,
		StatusOffline, formatTime(time.Now().UTC()), StatusOnline)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// PurgeOldData 按保留期清理历史数据，控制 SQLite 体积。删除早于 before 的：
//   - 终态 job 及其输出（pending/running 永不删，避免误清在途任务）
//   - 审计记录
//
// 返回删除的 job 数与审计数。before 为零值时跳过对应清理。
func (s *Store) PurgeOldData(ctx context.Context, jobBefore, auditBefore time.Time) (jobs int64, audits int64, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback() //nolint:errcheck

	if !jobBefore.IsZero() {
		cutoff := formatTime(jobBefore)
		terminal := []string{
			JobStatusSucceeded, JobStatusFailed, JobStatusTimeout, JobStatusExpired,
		}
		// 仅清理已终态的 job；输出行随 job 一起删（同事务，避免孤儿输出）。
		if _, err := tx.ExecContext(ctx, `
			DELETE FROM job_output
			WHERE job_id IN (
				SELECT job_id FROM jobs
				WHERE status IN (?, ?, ?, ?) AND finished_at IS NOT NULL AND finished_at < ?
			)`,
			terminal[0], terminal[1], terminal[2], terminal[3], cutoff); err != nil {
			return 0, 0, err
		}
		res2, err := tx.ExecContext(ctx, `
			DELETE FROM jobs
			WHERE status IN (?, ?, ?, ?) AND finished_at IS NOT NULL AND finished_at < ?`,
			terminal[0], terminal[1], terminal[2], terminal[3], cutoff)
		if err != nil {
			return 0, 0, err
		}
		jobs, _ = res2.RowsAffected()
	}

	if !auditBefore.IsZero() {
		res, err := tx.ExecContext(ctx, `DELETE FROM audit_logs WHERE at < ?`,
			formatTime(auditBefore))
		if err != nil {
			return 0, 0, err
		}
		audits, _ = res.RowsAffected()
	}

	if err := tx.Commit(); err != nil {
		return 0, 0, err
	}
	return jobs, audits, nil
}
