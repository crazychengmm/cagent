package jobworker

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/crazychengmm/cagent/internal/agent/executor"
	pb "github.com/crazychengmm/cagent/proto/cagent/v1"
)

// scriptedExecutor 按脚本返回事件并记录 Run 被调用的命令序列。
type scriptedExecutor struct {
	mu     sync.Mutex
	calls  []string
	script map[string][]executor.Event
}

func (e *scriptedExecutor) Run(_ context.Context, spec executor.Spec) <-chan executor.Event {
	e.mu.Lock()
	e.calls = append(e.calls, spec.Command)
	events := e.script[spec.Command]
	e.mu.Unlock()

	ch := make(chan executor.Event, 16)
	go func() {
		defer close(ch)
		ch <- executor.Event{Kind: executor.EventStarted}
		for _, ev := range events {
			ch <- ev
		}
	}()
	return ch
}

func (e *scriptedExecutor) callCount(command string) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	n := 0
	for _, c := range e.calls {
		if c == command {
			n++
		}
	}
	return n
}

func TestOnDispatch_ReplayCachedResultWithoutReexecute(t *testing.T) {
	t.Parallel()

	exec := &scriptedExecutor{script: map[string][]executor.Event{
		"do-once": {
			{Kind: executor.EventOutput, Stream: executor.StreamStdout, Data: []byte("v1")},
			{Kind: executor.EventFinished, Result: executor.ResultSucceeded, ExitCode: 0},
		},
	}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := NewManager(2, exec, discardLogger(), nil)
	m.Start(ctx)

	sender1 := &fakeSender{}
	m.OnDispatch(context.Background(), sender1,
		&pb.JobDispatch{JobId: "jx", Command: "do-once", TimeoutSeconds: 5})
	if fin := waitFinished(t, sender1, "jx", 3*time.Second); fin.GetResult() != pb.JobFinished_RESULT_SUCCEEDED {
		t.Fatalf("首次结果错误: %v", fin)
	}
	if exec.callCount("do-once") != 1 {
		t.Fatal("首次应执行一次")
	}

	// server 重发同一 job_id 到新的控制流：命中缓存，不得再执行。
	sender2 := &fakeSender{}
	m.OnDispatch(context.Background(), sender2,
		&pb.JobDispatch{JobId: "jx", Command: "do-once", TimeoutSeconds: 5})
	if fin := waitFinished(t, sender2, "jx", 3*time.Second); fin.GetResult() != pb.JobFinished_RESULT_SUCCEEDED {
		t.Fatalf("回放结果错误: %v", fin)
	}
	if exec.callCount("do-once") != 1 {
		t.Errorf("重发不得重新执行，Run 调用数 = %d", exec.callCount("do-once"))
	}
	if sender2.outputCount("jx") != 0 {
		t.Error("回放只回终态，不重放输出（server 已落库）")
	}
}

func TestResultCache_FIFOEviction(t *testing.T) {
	t.Parallel()

	c := newResultCache(2)
	fin := func() *pb.JobFinished { return &pb.JobFinished{Result: pb.JobFinished_RESULT_SUCCEEDED} }
	c.put("a", fin())
	c.put("b", fin())
	c.put("c", fin())

	if _, ok := c.get("a"); ok {
		t.Error("容量 2 时 a 应被淘汰")
	}
	if _, ok := c.get("b"); !ok {
		t.Error("b 应命中")
	}
	if _, ok := c.get("c"); !ok {
		t.Error("c 应命中")
	}
}

func TestResultCache_ZeroCapacityDefaults(t *testing.T) {
	t.Parallel()

	c := newResultCache(0)
	if c.capacity != defaultResultCacheCapacity {
		t.Errorf("默认容量 = %d", c.capacity)
	}
}
