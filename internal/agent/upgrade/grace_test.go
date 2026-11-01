package upgrade

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type fakeTracker struct {
	mu    sync.Mutex
	count int
}

func (f *fakeTracker) ActiveCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.count
}
func (f *fakeTracker) set(n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.count = n
}

func TestWaitForIdle_AlreadyIdle(t *testing.T) {
	t.Parallel()

	tr := &fakeTracker{}
	start := time.Now()
	if err := WaitForIdle(context.Background(), tr, 2*time.Second); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 50*time.Millisecond {
		t.Error("已空闲应立即返回")
	}
}

func TestWaitForIdle_BecomesIdle(t *testing.T) {
	t.Parallel()

	tr := &fakeTracker{count: 1}
	go func() {
		time.Sleep(250 * time.Millisecond)
		tr.set(0)
	}()
	if err := WaitForIdle(context.Background(), tr, 2*time.Second); err != nil {
		t.Fatalf("应等待到空闲: %v", err)
	}
}

func TestWaitForIdle_TimeoutDoesNotKill(t *testing.T) {
	t.Parallel()

	tr := &fakeTracker{count: 2}
	start := time.Now()
	err := WaitForIdle(context.Background(), tr, 300*time.Millisecond)
	if !errors.Is(err, ErrJobsRunning) {
		t.Fatalf("应 ErrJobsRunning，实际 %v", err)
	}
	if time.Since(start) < 250*time.Millisecond {
		t.Error("不应提前超时")
	}
	if tr.ActiveCount() != 2 {
		t.Error("grace 不得影响任务本身")
	}
}

func TestWaitForIdle_ContextCancel(t *testing.T) {
	t.Parallel()

	tr := &fakeTracker{count: 1}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()
	if err := WaitForIdle(ctx, tr, 5*time.Second); !errors.Is(err, context.Canceled) {
		t.Errorf("期望 ctx 取消，实际 %v", err)
	}
}
