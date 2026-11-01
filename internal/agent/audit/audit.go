// Package audit 在 agent 本地以 JSON Lines 追加记录每次任务执行，
// 与 server 侧持久化审计互为补充（agent 端可追溯，即使与 server 断连）。
package audit

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	dirPerm   os.FileMode = 0o700
	filePerm  os.FileMode = 0o600
	auditFile             = "audit.log"
)

// FileLogger 是并发安全的 JSONL 审计记录器。
type FileLogger struct {
	mu sync.Mutex
	f  *os.File
}

// OpenFileLogger 打开（必要时创建）dataDir 下的 audit.log，目录 0700、文件 0600。
func OpenFileLogger(dataDir string) (*FileLogger, error) {
	if err := os.MkdirAll(dataDir, dirPerm); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dataDir, auditFile),
		os.O_APPEND|os.O_CREATE|os.O_WRONLY, filePerm)
	if err != nil {
		return nil, err
	}
	return &FileLogger{f: f}, nil
}

// JobStart 是一次执行的开始记录。
type JobStart struct {
	TS          time.Time `json:"ts"`
	Event       string    `json:"event"`
	JobID       string    `json:"job_id"`
	RequestedBy string    `json:"requested_by"`
	Command     string    `json:"command"`
	WorkingDir  string    `json:"working_dir"`
}

// JobFinish 是一次执行的结束记录（含输出预览，不含完整输出）。
type JobFinish struct {
	TS            time.Time `json:"ts"`
	Event         string    `json:"event"`
	JobID         string    `json:"job_id"`
	Result        string    `json:"result"`
	ExitCode      int       `json:"exit_code"`
	Truncated     bool      `json:"truncated"`
	OutputPreview string    `json:"output_preview"`
}

// Path 返回审计文件路径（供文档/测试引用）。
func Path(dataDir string) string {
	return filepath.Join(dataDir, auditFile)
}

func (l *FileLogger) write(entry any) error {
	line, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	line = append(line, '\n')

	l.mu.Lock()
	defer l.mu.Unlock()
	_, err = l.f.Write(line)
	return err
}

// JobStarted 追加开始记录。
func (l *FileLogger) JobStarted(e JobStart) error {
	e.Event = "job.started"
	return l.write(e)
}

// JobFinished 追加结束记录。
func (l *FileLogger) JobFinished(e JobFinish) error {
	e.Event = "job.finished"
	return l.write(e)
}

// Close 关闭文件。
func (l *FileLogger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return nil
	}
	err := l.f.Close()
	l.f = nil
	return err
}

var _ io.Closer = (*FileLogger)(nil)
