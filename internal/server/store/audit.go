package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// 审计动作常量（detail JSON 的结构由各业务方自行约定）。
const (
	ActionTokenCreate     = "token.create"
	ActionTokenRevoke     = "token.revoke"
	ActionAgentEnroll     = "agent.enroll"
	ActionAgentRejected   = "agent.enroll_rejected"
	ActionJobCreate       = "job.create"
	ActionJobFinish       = "job.finish"
	ActionReleasePublish  = "release.publish"
	ActionUpgradeDispatch = "upgrade.dispatch"
)

// AuditLog 是 audit_logs 表的一行。
type AuditLog struct {
	ID      int64
	At      time.Time
	Actor   string
	Action  string
	AgentID string
	JobID   string
	Detail  []byte
}

// InsertAudit 追加一条审计；空的 AgentID/JobID 存 NULL，detail 为空时存 {}。
func (s *Store) InsertAudit(ctx context.Context, l AuditLog) error {
	detail := l.Detail
	if len(detail) == 0 {
		detail = []byte("{}")
	}
	if !json.Valid(detail) {
		return fmt.Errorf("审计 detail 不是合法 JSON: %q", string(detail))
	}

	var agentID, jobID any
	if l.AgentID != "" {
		agentID = l.AgentID
	}
	if l.JobID != "" {
		jobID = l.JobID
	}

	if l.At.IsZero() {
		l.At = time.Now()
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO audit_logs (at, actor, action, agent_id, job_id, detail)
VALUES (?, ?, ?, ?, ?, ?)`,
		formatTime(l.At), l.Actor, l.Action, agentID, jobID, string(detail))
	if err != nil {
		return fmt.Errorf("写入审计 %s: %w", l.Action, err)
	}
	return nil
}

// ListAudits 按时间倒序返回最近 limit 条审计。
func (s *Store) ListAudits(ctx context.Context, limit int) ([]AuditLog, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT id, at, actor, action, agent_id, job_id, detail
FROM audit_logs ORDER BY at DESC, id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("查询审计: %w", err)
	}
	defer rows.Close()

	var logs []AuditLog
	for rows.Next() {
		var (
			l       AuditLog
			at      string
			agentID sql.NullString
			jobID   sql.NullString
			detail  string
		)
		if err := rows.Scan(&l.ID, &at, &l.Actor, &l.Action, &agentID, &jobID, &detail); err != nil {
			return nil, fmt.Errorf("扫描审计: %w", err)
		}
		if l.At, err = time.Parse(time.RFC3339Nano, at); err != nil {
			return nil, err
		}
		l.AgentID = agentID.String
		l.JobID = jobID.String
		l.Detail = []byte(detail)
		logs = append(logs, l)
	}
	return logs, rows.Err()
}
