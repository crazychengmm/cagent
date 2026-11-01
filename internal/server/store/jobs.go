package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// job 类型常量。
const (
	KindCommand = "command"
	KindUpgrade = "upgrade"
)

// job 状态常量（PENDING/EXPIRED 只存在于 server 侧）。
const (
	JobStatusPending   = "pending"
	JobStatusRunning   = "running"
	JobStatusSucceeded = "succeeded"
	JobStatusFailed    = "failed"
	JobStatusTimeout   = "timeout"
	JobStatusCanceled  = "canceled"
	JobStatusExpired   = "expired"
)

// 输出流常量。
const (
	StreamStdout = "stdout"
	StreamStderr = "stderr"
)

// TerminalJobStatuses 是全部终态；终态记录不可再流转。
var TerminalJobStatuses = []string{
	JobStatusSucceeded, JobStatusFailed, JobStatusTimeout,
	JobStatusCanceled, JobStatusExpired,
}

func isTerminal(status string) bool {
	for _, t := range TerminalJobStatuses {
		if status == t {
			return true
		}
	}
	return false
}

// Job 是 jobs 表的一行。
type Job struct {
	JobID          string
	AgentID        string
	Kind           string
	Command        string
	WorkingDir     string
	ExtraEnv       map[string]string
	TimeoutSeconds int64
	RequestedBy    string
	Status         string
	ExitCode       *int
	Truncated      bool
	Message        string
	CreatedAt      time.Time
	StartedAt      *time.Time
	FinishedAt     *time.Time
}

// JobOutputChunk 是一段流式输出。
type JobOutputChunk struct {
	Seq    int64
	Stream string
	Data   []byte
}

// ListJobsFilter 按可空字段过滤；Limit<=0 时取默认 100。
type ListJobsFilter struct {
	AgentID string
	Status  string
	Kind    string
	Limit   int
}

// CreateJob 插入新 job；job_id 重复或 agent 不存在（外键）时返回错误。
func (s *Store) CreateJob(ctx context.Context, j Job) error {
	env, err := json.Marshal(j.ExtraEnv)
	if err != nil {
		return fmt.Errorf("序列化 extra_env: %w", err)
	}
	_, err = s.db.ExecContext(ctx, `
INSERT INTO jobs (job_id, agent_id, kind, command, working_dir, extra_env,
                  timeout_seconds, requested_by, status, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		j.JobID, j.AgentID, j.Kind, j.Command, j.WorkingDir, string(env),
		j.TimeoutSeconds, j.RequestedBy, j.Status,
		formatTime(j.CreatedAt), formatTime(j.CreatedAt))
	if err != nil {
		return fmt.Errorf("插入 job %s: %w", j.JobID, err)
	}
	return nil
}

// GetJob 查询单个 job；不存在返回 ErrNotFound。
func (s *Store) GetJob(ctx context.Context, jobID string) (*Job, error) {
	row := s.db.QueryRowContext(ctx, jobSelect+` WHERE job_id = ?`, jobID)
	return scanJob(row)
}

// ListJobs 按过滤条件列出 job，按创建时间倒序。
func (s *Store) ListJobs(ctx context.Context, f ListJobsFilter) ([]Job, error) {
	var clauses []string
	var args []any
	if f.AgentID != "" {
		clauses = append(clauses, "agent_id = ?")
		args = append(args, f.AgentID)
	}
	if f.Status != "" {
		clauses = append(clauses, "status = ?")
		args = append(args, f.Status)
	}
	if f.Kind != "" {
		clauses = append(clauses, "kind = ?")
		args = append(args, f.Kind)
	}
	limit := f.Limit
	if limit <= 0 {
		limit = 100
	}

	query := jobSelect
	if len(clauses) > 0 {
		query += " WHERE " + strings.Join(clauses, " AND ")
	}
	query += " ORDER BY created_at DESC LIMIT ?"
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("查询 jobs: %w", err)
	}
	defer rows.Close()

	var jobs []Job
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, *j)
	}
	return jobs, rows.Err()
}

// UpdateJobState 在记录非终态时推进状态；记录已终态返回 (false, nil)。
// 进入 running 记 started_at，进入终态记 finished_at。
func (s *Store) UpdateJobState(ctx context.Context, jobID, status string,
	exitCode *int, truncated bool, message string, at time.Time) (bool, error) {
	if status != JobStatusRunning && !isTerminal(status) {
		return false, fmt.Errorf("非法目标状态 %q", status)
	}

	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(TerminalJobStatuses)), ",")
	query := `
UPDATE jobs
SET status = ?, exit_code = ?, truncated = ?, message = ?,
    started_at = CASE WHEN ? = 'running' THEN ? ELSE started_at END,
    finished_at = CASE WHEN ? IN (` + placeholders + `) THEN ? ELSE finished_at END,
    updated_at = ?
WHERE job_id = ? AND status NOT IN (` + placeholders + `)`

	var exitArg any
	if exitCode != nil {
		exitArg = *exitCode
	}
	truncatedArg := 0
	if truncated {
		truncatedArg = 1
	}

	args := []any{
		status, exitArg, truncatedArg, message, // SET
		status, formatTime(at), // started_at CASE
		status, // finished_at CASE 输入
	}
	for _, t := range TerminalJobStatuses { // finished_at IN 列表
		args = append(args, t)
	}
	args = append(args,
		formatTime(at), // finished_at THEN
		formatTime(at), // updated_at
		jobID,          // WHERE job_id
	)
	for _, t := range TerminalJobStatuses { // WHERE status NOT IN
		args = append(args, t)
	}

	res, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return false, fmt.Errorf("更新 job 状态 %s: %w", jobID, err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// AppendJobOutput 追加一段输出，seq 由调用方保证单调。
func (s *Store) AppendJobOutput(ctx context.Context, jobID string, chunk JobOutputChunk) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO job_output (job_id, seq, stream, data) VALUES (?, ?, ?, ?)`,
		jobID, chunk.Seq, chunk.Stream, chunk.Data)
	if err != nil {
		return fmt.Errorf("追加 job 输出 %s#%d: %w", jobID, chunk.Seq, err)
	}
	return nil
}

// GetJobOutput 按 seq 顺序返回 job 的全部输出块。
func (s *Store) GetJobOutput(ctx context.Context, jobID string) ([]JobOutputChunk, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT seq, stream, data FROM job_output WHERE job_id = ? ORDER BY seq`, jobID)
	if err != nil {
		return nil, fmt.Errorf("查询 job 输出 %s: %w", jobID, err)
	}
	defer rows.Close()

	var chunks []JobOutputChunk
	for rows.Next() {
		var c JobOutputChunk
		if err := rows.Scan(&c.Seq, &c.Stream, &c.Data); err != nil {
			return nil, fmt.Errorf("扫描 job 输出: %w", err)
		}
		chunks = append(chunks, c)
	}
	return chunks, rows.Err()
}

// ExpirePendingJobsBefore 把创建时间早于 cutoff 的 pending job 置为 expired，
// 返回受影响行数。时间列是 UTC RFC3339 文本，字典序与时间序一致。
func (s *Store) ExpirePendingJobsBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `
UPDATE jobs
SET status = ?, finished_at = ?, updated_at = ?
WHERE status = ? AND created_at < ?`,
		JobStatusExpired, formatTime(time.Now().UTC()), formatTime(time.Now().UTC()),
		JobStatusPending, formatTime(cutoff))
	if err != nil {
		return 0, fmt.Errorf("过期 pending jobs: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// SumJobOutputSize 返回已保存输出的总字节数（截断判定用）。
func (s *Store) SumJobOutputSize(ctx context.Context, jobID string) (int64, error) {
	var size sql.NullInt64
	if err := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(length(data)), 0) FROM job_output WHERE job_id = ?`,
		jobID).Scan(&size); err != nil {
		return 0, fmt.Errorf("统计 job 输出大小 %s: %w", jobID, err)
	}
	return size.Int64, nil
}

const jobSelect = `
SELECT job_id, agent_id, kind, command, working_dir, extra_env, timeout_seconds,
       requested_by, status, exit_code, truncated, message,
       created_at, started_at, finished_at
FROM jobs`

func scanJob(row rowScanner) (*Job, error) {
	var (
		j          Job
		envJSON    string
		exitCode   sql.NullInt64
		truncated  int
		createdAt  string
		startedAt  sql.NullString
		finishedAt sql.NullString
	)
	err := row.Scan(&j.JobID, &j.AgentID, &j.Kind, &j.Command, &j.WorkingDir, &envJSON,
		&j.TimeoutSeconds, &j.RequestedBy, &j.Status, &exitCode, &truncated, &j.Message,
		&createdAt, &startedAt, &finishedAt)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("扫描 job: %w", err)
	}

	if err := json.Unmarshal([]byte(envJSON), &j.ExtraEnv); err != nil {
		return nil, fmt.Errorf("解析 extra_env: %w", err)
	}
	if exitCode.Valid {
		code := int(exitCode.Int64)
		j.ExitCode = &code
	}
	j.Truncated = truncated != 0
	if j.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt); err != nil {
		return nil, err
	}
	if j.StartedAt, err = parseOptionalTime(startedAt); err != nil {
		return nil, err
	}
	if j.FinishedAt, err = parseOptionalTime(finishedAt); err != nil {
		return nil, err
	}
	return &j, nil
}
