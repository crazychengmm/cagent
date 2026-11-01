package jobworker

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	agentaudit "github.com/crazychengmm/cagent/internal/agent/audit"
	"github.com/crazychengmm/cagent/internal/agent/executor"
	pb "github.com/crazychengmm/cagent/proto/cagent/v1"
)

type recordingAuditor struct {
	mu       sync.Mutex
	starts   []agentaudit.JobStart
	finishes []agentaudit.JobFinish
}

func (r *recordingAuditor) JobStarted(e agentaudit.JobStart) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.starts = append(r.starts, e)
	return nil
}

func (r *recordingAuditor) JobFinished(e agentaudit.JobFinish) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.finishes = append(r.finishes, e)
	return nil
}

func (r *recordingAuditor) finish(jobID string) (agentaudit.JobFinish, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, f := range r.finishes {
		if f.JobID == jobID {
			return f, true
		}
	}
	return agentaudit.JobFinish{}, false
}

func newAuditedManager(t *testing.T, auditor Auditor) (*Manager, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	m := NewManager(2, executor.NewLocalExecutor(),
		slog.New(slog.NewTextHandler(io.Discard, nil)), auditor)
	m.Start(ctx)
	t.Cleanup(func() {
		cancel()
		m.Wait()
	})
	return m, cancel
}

func TestManager_AuditsStartAndFinish(t *testing.T) {
	t.Parallel()

	auditor := &recordingAuditor{}
	m, _ := newAuditedManager(t, auditor)
	dir := t.TempDir()
	sender := &fakeSender{}
	m.OnDispatch(context.Background(), sender, &pb.JobDispatch{
		JobId: "au-1", Command: "printf hello", WorkingDir: dir,
		RequestedBy: "bob", TimeoutSeconds: 5,
	})
	waitFinished(t, sender, "au-1", 3*time.Second)

	auditor.mu.Lock()
	gotStarts := append([]agentaudit.JobStart(nil), auditor.starts...)
	auditor.mu.Unlock()
	if len(gotStarts) != 1 || gotStarts[0].Command != "printf hello" ||
		gotStarts[0].RequestedBy != "bob" || gotStarts[0].WorkingDir != dir {
		t.Errorf("开始审计错误: %+v", gotStarts)
	}
	fin, ok := auditor.finish("au-1")
	if !ok || fin.Result != string(executor.ResultSucceeded) || fin.ExitCode != 0 ||
		fin.OutputPreview != "hello" {
		t.Errorf("结束审计错误: %+v ok=%v", fin, ok)
	}
}

func TestManager_AuditPreviewCapped(t *testing.T) {
	t.Parallel()

	auditor := &recordingAuditor{}
	m, _ := newAuditedManager(t, auditor)
	sender := &fakeSender{}
	// 产生 5000 字节输出，审计预览应只保留前 4096。
	m.OnDispatch(context.Background(), sender, &pb.JobDispatch{
		JobId: "au-2", Command: "head -c 5000 /dev/zero | tr '\\0' 'a'", TimeoutSeconds: 5,
	})
	waitFinished(t, sender, "au-2", 5*time.Second)

	fin, ok := auditor.finish("au-2")
	if !ok {
		t.Fatal("缺少结束审计")
	}
	if len(fin.OutputPreview) != outputPreviewLimit {
		t.Errorf("预览长度 = %d, 期望 %d", len(fin.OutputPreview), outputPreviewLimit)
	}
}

func TestManager_AuditsQueuedCancel(t *testing.T) {
	t.Parallel()

	auditor := &recordingAuditor{}
	m, _ := newAuditedManager(t, auditor)
	sender := &fakeSender{}
	m.OnDispatch(context.Background(), sender, &pb.JobDispatch{
		JobId: "au-3", Command: "sleep 0.5", TimeoutSeconds: 10,
	})
	time.Sleep(100 * time.Millisecond)
	m.OnDispatch(context.Background(), sender, &pb.JobDispatch{
		JobId: "au-4", Command: "printf queued", TimeoutSeconds: 10,
	})
	m.OnCancel("au-4")
	waitFinished(t, sender, "au-4", 3*time.Second)

	fin, ok := auditor.finish("au-4")
	if !ok || fin.Result != string(executor.ResultCanceled) {
		t.Errorf("排队取消审计错误: %+v ok=%v", fin, ok)
	}
}
