package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Agent 在线状态常量。
const (
	StatusOnline  = "online"
	StatusOffline = "offline"
)

// ErrNotFound 表示按主键查询无记录。
var ErrNotFound = errors.New("记录不存在")

// Agent 是 agents 表的一行。
type Agent struct {
	AgentID           string
	Hostname          string
	Labels            map[string]string
	Version           string
	Status            string
	CertificateSerial string
	CertNotAfter      *time.Time
	LastHeartbeatAt   *time.Time
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// InsertAgent 写入新注册的 agent；agent_id 冲突时返回错误。
func (s *Store) InsertAgent(ctx context.Context, a Agent) error {
	labels, err := json.Marshal(a.Labels)
	if err != nil {
		return fmt.Errorf("序列化 labels: %w", err)
	}
	_, err = s.db.ExecContext(ctx, `
INSERT INTO agents (agent_id, hostname, labels, version, status, certificate_serial,
                    cert_not_after, last_heartbeat_at, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		a.AgentID, a.Hostname, string(labels), a.Version, StatusOffline, a.CertificateSerial,
		nullableTime(a.CertNotAfter), nullableTime(a.LastHeartbeatAt),
		formatTime(a.CreatedAt), formatTime(a.UpdatedAt))
	if err != nil {
		return fmt.Errorf("插入 agent %s: %w", a.AgentID, err)
	}
	return nil
}

// GetAgent 按 UUID 查询；不存在返回 ErrNotFound。
func (s *Store) GetAgent(ctx context.Context, agentID string) (*Agent, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT agent_id, hostname, labels, version, status, certificate_serial,
       cert_not_after, last_heartbeat_at, created_at, updated_at
FROM agents WHERE agent_id = ?`, agentID)
	return scanAgent(row)
}

// ListAgents 返回全部 agent，按最近心跳时间倒序（从未心跳的排最后）。
func (s *Store) ListAgents(ctx context.Context) ([]Agent, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT agent_id, hostname, labels, version, status, certificate_serial,
       cert_not_after, last_heartbeat_at, created_at, updated_at
FROM agents ORDER BY last_heartbeat_at IS NULL, last_heartbeat_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("列出 agents: %w", err)
	}
	defer rows.Close()

	var agents []Agent
	for rows.Next() {
		a, err := scanAgent(rows)
		if err != nil {
			return nil, err
		}
		agents = append(agents, *a)
	}
	return agents, rows.Err()
}

// RecordHeartbeat 用心跳内容刷新版本/标签并置 online；agent 不存在返回 ErrNotFound。
func (s *Store) RecordHeartbeat(ctx context.Context, agentID, version string,
	labels map[string]string, at time.Time) error {
	labelsJSON, err := json.Marshal(labels)
	if err != nil {
		return fmt.Errorf("序列化 labels: %w", err)
	}
	res, err := s.db.ExecContext(ctx, `
UPDATE agents
SET version = ?, labels = ?, status = ?, last_heartbeat_at = ?, updated_at = ?
WHERE agent_id = ?`,
		version, string(labelsJSON), StatusOnline, formatTime(at), formatTime(at), agentID)
	if err != nil {
		return fmt.Errorf("更新心跳 %s: %w", agentID, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("agent %s: %w", agentID, ErrNotFound)
	}
	return nil
}

// SetAgentStatus 更新在线状态（离线扫描使用）。
func (s *Store) SetAgentStatus(ctx context.Context, agentID, status string, at time.Time) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE agents SET status = ?, updated_at = ? WHERE agent_id = ?`,
		status, formatTime(at), agentID)
	if err != nil {
		return fmt.Errorf("更新 agent 状态 %s: %w", agentID, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("agent %s: %w", agentID, ErrNotFound)
	}
	return nil
}

// ListAgentsOlderThan 返回最后心跳早于 cutoff 的 online agent（离线扫描用）。
func (s *Store) ListAgentsOlderThan(ctx context.Context, cutoff time.Time) ([]Agent, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT agent_id, hostname, labels, version, status, certificate_serial,
       cert_not_after, last_heartbeat_at, created_at, updated_at
FROM agents
WHERE status = ? AND last_heartbeat_at IS NOT NULL AND last_heartbeat_at < ?`,
		StatusOnline, formatTime(cutoff))
	if err != nil {
		return nil, fmt.Errorf("扫描超时 agent: %w", err)
	}
	defer rows.Close()

	var agents []Agent
	for rows.Next() {
		a, err := scanAgent(rows)
		if err != nil {
			return nil, err
		}
		agents = append(agents, *a)
	}
	return agents, rows.Err()
}

// rowScanner 同时兼容 *sql.Row 与 *sql.Rows。
type rowScanner interface {
	Scan(dest ...any) error
}

func scanAgent(row rowScanner) (*Agent, error) {
	var (
		a             Agent
		labelsJSON    string
		serial        sql.NullString
		certNotAfter  sql.NullString
		lastHeartbeat sql.NullString
		createdAt     string
		updatedAt     string
	)
	err := row.Scan(&a.AgentID, &a.Hostname, &labelsJSON, &a.Version, &a.Status, &serial,
		&certNotAfter, &lastHeartbeat, &createdAt, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("扫描 agent: %w", err)
	}

	if err := json.Unmarshal([]byte(labelsJSON), &a.Labels); err != nil {
		return nil, fmt.Errorf("解析 labels: %w", err)
	}
	a.CertificateSerial = serial.String
	if a.CertNotAfter, err = parseOptionalTime(certNotAfter); err != nil {
		return nil, err
	}
	if a.LastHeartbeatAt, err = parseOptionalTime(lastHeartbeat); err != nil {
		return nil, err
	}
	if a.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt); err != nil {
		return nil, err
	}
	if a.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedAt); err != nil {
		return nil, err
	}
	return &a, nil
}
