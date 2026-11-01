package jobworker

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/crazychengmm/cagent/internal/agent/control"
	"github.com/crazychengmm/cagent/internal/agent/executor"
	pb "github.com/crazychengmm/cagent/proto/cagent/v1"
)

type fakeSender struct {
	mu   sync.Mutex
	msgs []*pb.AgentMessage
}

func (f *fakeSender) Send(msg *pb.AgentMessage) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.msgs = append(f.msgs, msg)
	return nil
}

func (f *fakeSender) finished(jobID string) *pb.JobFinished {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.msgs) - 1; i >= 0; i-- {
		if ev := f.msgs[i].GetJobEvent(); ev != nil && ev.GetJobId() == jobID {
			if fin := ev.GetFinished(); fin != nil {
				return fin
			}
		}
	}
	return nil
}

func (f *fakeSender) outputCount(jobID string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, m := range f.msgs {
		if ev := m.GetJobEvent(); ev != nil && ev.GetJobId() == jobID && ev.GetOutput() != nil {
			n++
		}
	}
	return n
}

func waitFinished(t *testing.T, sender *fakeSender, jobID string, timeout time.Duration) *pb.JobFinished {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fin := sender.finished(jobID); fin != nil {
			return fin
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("任务 %s 未在 %s 内结束", jobID, timeout)
	return nil
}

func newTestManager(t *testing.T, concurrency int) (*Manager, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	m := NewManager(concurrency, executor.NewLocalExecutor(),
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	m.Start(ctx)
	t.Cleanup(func() {
		cancel()
		m.Wait()
	})
	return m, cancel
}

func dispatch(jobID, command string, timeoutSec int64) *pb.JobDispatch {
	return &pb.JobDispatch{JobId: jobID, Command: command, TimeoutSeconds: timeoutSec}
}

var _ control.Sender = (*fakeSender)(nil)

func TestManager_AllJobsComplete(t *testing.T) {
	t.Parallel()

	m, _ := newTestManager(t, 2)
	sender := &fakeSender{}
	for i := 0; i < 5; i++ {
		m.OnDispatch(context.Background(), sender,
			dispatch("j"+string(rune('a'+i)), "printf out", 5))
	}
	for _, id := range []string{"ja", "jb", "jc", "jd", "je"} {
		fin := waitFinished(t, sender, id, 3*time.Second)
		if fin.Result != pb.JobFinished_RESULT_SUCCEEDED || fin.ExitCode != 0 {
			t.Errorf("%s 结果不符: %+v", id, fin)
		}
		if sender.outputCount(id) == 0 {
			t.Errorf("%s 应有输出事件", id)
		}
	}
}

func TestManager_ConcurrencyLimit(t *testing.T) {
	t.Parallel()

	m, _ := newTestManager(t, 2)
	sender := &fakeSender{}
	start := time.Now()
	for _, id := range []string{"c1", "c2", "c3"} {
		m.OnDispatch(context.Background(), sender, dispatch(id, "sleep 0.3", 10))
	}
	for _, id := range []string{"c1", "c2", "c3"} {
		if waitFinished(t, sender, id, 5*time.Second).Result != pb.JobFinished_RESULT_SUCCEEDED {
			t.Fatalf("%s 应成功", id)
		}
	}
	elapsed := time.Since(start)
	// 并发 2：3 个 0.3s 任务约 0.6s；串行会 0.9s，全并行 0.3s。
	if elapsed < 450*time.Millisecond {
		t.Errorf("耗时 %s <450ms，并发上限可能未生效", elapsed)
	}
	if elapsed > 850*time.Millisecond {
		t.Errorf("耗时 %s >850ms，疑似退化为串行", elapsed)
	}
}

func TestManager_CancelRunning(t *testing.T) {
	t.Parallel()

	m, _ := newTestManager(t, 1)
	sender := &fakeSender{}
	m.OnDispatch(context.Background(), sender, dispatch("k1", "sleep 20", 30))
	time.Sleep(150 * time.Millisecond)
	m.OnCancel("k1")

	fin := waitFinished(t, sender, "k1", 3*time.Second)
	if fin.Result != pb.JobFinished_RESULT_CANCELED {
		t.Errorf("期望 CANCELED，实际 %v", fin.Result)
	}
}

func TestManager_CancelQueuedDoesNotExecute(t *testing.T) {
	t.Parallel()

	m, _ := newTestManager(t, 1)
	sender := &fakeSender{}
	m.OnDispatch(context.Background(), sender, dispatch("q1", "sleep 0.5", 10))
	time.Sleep(100 * time.Millisecond) // 占住唯一 worker
	m.OnDispatch(context.Background(), sender, dispatch("q2", "printf should-not-run", 10))
	m.OnCancel("q2")

	fin := waitFinished(t, sender, "q2", 3*time.Second)
	if fin.Result != pb.JobFinished_RESULT_CANCELED {
		t.Errorf("排队任务取消应 CANCELED，实际 %v", fin.Result)
	}
	if sender.outputCount("q2") > 0 {
		t.Error("排队中取消的任务不应执行产生输出")
	}
	if waitFinished(t, sender, "q1", 3*time.Second).Result != pb.JobFinished_RESULT_SUCCEEDED {
		t.Error("q1 应正常成功")
	}
}

func TestManager_TimeoutMapsToResult(t *testing.T) {
	t.Parallel()

	m, _ := newTestManager(t, 1)
	sender := &fakeSender{}
	m.OnDispatch(context.Background(), sender, dispatch("t1", "sleep 20", 1))
	fin := waitFinished(t, sender, "t1", 3*time.Second)
	if fin.Result != pb.JobFinished_RESULT_TIMEOUT {
		t.Errorf("1s 超时应 TIMEOUT，实际 %v", fin.Result)
	}
}

func TestManager_DuplicateDispatchIgnored(t *testing.T) {
	t.Parallel()

	m, _ := newTestManager(t, 1)
	sender := &fakeSender{}
	m.OnDispatch(context.Background(), sender, dispatch("d1", "sleep 0.3", 10))
	time.Sleep(100 * time.Millisecond)
	m.OnDispatch(context.Background(), sender, dispatch("d1", "sleep 0.3", 10))
	if waitFinished(t, sender, "d1", 3*time.Second).Result != pb.JobFinished_RESULT_SUCCEEDED {
		t.Fatal("应成功")
	}
	// 同一个 job 只应有一个 Started。
	starts := 0
	for _, msg := range sender.msgs {
		if ev := msg.GetJobEvent(); ev != nil && ev.GetJobId() == "d1" && ev.GetStarted() != nil {
			starts++
		}
	}
	if starts != 1 {
		t.Errorf("重复派发被执行，Started 数 = %d", starts)
	}
}
