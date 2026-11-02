package upgrade

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	pb "github.com/crazychengmm/cagent/proto/cagent/v1"
)

type fakeInstaller struct {
	mu          sync.Mutex
	calls       []string
	stageErr    error
	activateErr error
	restartErr  error
	rollbackErr error
	commitErr   error
}

func (f *fakeInstaller) Stage([]byte) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "stage")
	return "/tmp/staged", f.stageErr
}
func (f *fakeInstaller) Activate(string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "activate")
	return f.activateErr
}
func (f *fakeInstaller) Rollback() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "rollback")
	return f.rollbackErr
}
func (f *fakeInstaller) Restart() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	prior := 0
	for _, c := range f.calls {
		if c == "restart" {
			prior++
		}
	}
	f.calls = append(f.calls, "restart")
	// restartErr 仅在第一次重启时返回（回滚后的第二次重启成功）。
	if f.restartErr != nil && prior == 0 {
		return f.restartErr
	}
	return nil
}
func (f *fakeInstaller) Commit() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "commit")
	return f.commitErr
}

type fakeProbe struct{ err error }

func (p *fakeProbe) WaitHealthy(context.Context, time.Duration) error { return p.err }

type fakeReporter struct {
	mu     sync.Mutex
	events []*pb.UpgradeEvent
}

func (r *fakeReporter) Report(e *pb.UpgradeEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
}
func (r *fakeReporter) statuses() []pb.UpgradeEvent_Status {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []pb.UpgradeEvent_Status
	for _, e := range r.events {
		out = append(out, e.GetStatus())
	}
	return out
}

func command(version string, allowDowngrade bool) *pb.UpgradeCommand {
	return &pb.UpgradeCommand{JobId: "u-job", Version: version, AllowDowngrade: allowDowngrade}
}

func happyDeps(inst *fakeInstaller) (Deps, *fakeReporter) {
	reporter := &fakeReporter{}
	return Deps{
		Tracker:       &fakeTracker{count: 0},
		Installer:     inst,
		Probe:         &fakeProbe{},
		Reporter:      reporter,
		GraceTimeout:  time.Second,
		HealthTimeout: time.Second,
	}, reporter
}

func TestRun_HappyPath(t *testing.T) {
	t.Parallel()

	inst := &fakeInstaller{}
	deps, reporter := happyDeps(inst)
	result := Run(context.Background(), "u-job", "1.0.0", command("2.0.0", false),
		[]byte("pkg"), deps)

	if result.State != StateSucceeded {
		t.Fatalf("state=%s reason=%s", result.State, result.Reason)
	}
	wantCalls := []string{"stage", "activate", "restart", "commit"}
	for _, want := range wantCalls {
		found := false
		for _, c := range inst.calls {
			if c == want {
				found = true
			}
		}
		if !found {
			t.Errorf("缺少调用 %s，实际 %v", want, inst.calls)
		}
	}
	// 结束状态由调用方持久化（PendingResult），主连接仅即时报 INSTALLING。
	statuses := reporter.statuses()
	if len(statuses) != 1 || statuses[0] != pb.UpgradeEvent_STATUS_INSTALLING {
		t.Errorf("主进程只应即时报 INSTALLING: %v", statuses)
	}
}

func TestRun_DowngradeRejected(t *testing.T) {
	t.Parallel()

	inst := &fakeInstaller{}
	deps, reporter := happyDeps(inst)
	result := Run(context.Background(), "u-job", "2.0.0", command("1.0.0", false),
		[]byte("pkg"), deps)

	if result.State != StateFailed {
		t.Fatalf("state=%s", result.State)
	}
	if len(inst.calls) != 0 {
		t.Errorf("降级拒绝不应触碰安装器，实际 %v", inst.calls)
	}
	if statuses := reporter.statuses(); len(statuses) != 1 || statuses[0] != pb.UpgradeEvent_STATUS_FAILED {
		t.Errorf("应只有 FAILED 事件: %v", statuses)
	}
}

func TestRun_DowngradeExplicitlyAllowed(t *testing.T) {
	t.Parallel()

	inst := &fakeInstaller{}
	deps, _ := happyDeps(inst)
	result := Run(context.Background(), "u-job", "2.0.0", command("1.0.0", true),
		[]byte("pkg"), deps)
	if result.State != StateSucceeded {
		t.Fatalf("显式允许降级应成功: %s %s", result.State, result.Reason)
	}
}

func TestRun_GraceTimeout(t *testing.T) {
	t.Parallel()

	inst := &fakeInstaller{}
	deps, _ := happyDeps(inst)
	deps.Tracker = &fakeTracker{count: 2}
	deps.GraceTimeout = 50 * time.Millisecond

	result := Run(context.Background(), "u-job", "1.0.0", command("2.0.0", false),
		[]byte("pkg"), deps)
	if result.State != StateFailed {
		t.Fatalf("state=%s", result.State)
	}
	if len(inst.calls) != 0 {
		t.Errorf("grace 超时不应触碰安装器: %v", inst.calls)
	}
}

func TestRun_StageFailure(t *testing.T) {
	t.Parallel()

	inst := &fakeInstaller{stageErr: errors.New("disk full")}
	deps, _ := happyDeps(inst)
	result := Run(context.Background(), "u-job", "1.0.0", command("2.0.0", false),
		[]byte("pkg"), deps)
	if result.State != StateFailed {
		t.Fatalf("state=%s", result.State)
	}
	for _, c := range inst.calls {
		if c == "activate" || c == "restart" {
			t.Error("Stage 失败后不应继续")
		}
	}
}

func TestRun_ActivateFailure(t *testing.T) {
	t.Parallel()

	inst := &fakeInstaller{activateErr: errors.New("busy")}
	deps, _ := happyDeps(inst)
	result := Run(context.Background(), "u-job", "1.0.0", command("2.0.0", false),
		[]byte("pkg"), deps)
	if result.State != StateFailed {
		t.Fatalf("state=%s", result.State)
	}
}

func TestRun_RestartFailureRollsBack(t *testing.T) {
	t.Parallel()

	inst := &fakeInstaller{restartErr: errors.New("systemctl failed")}
	deps, _ := happyDeps(inst)
	result := Run(context.Background(), "u-job", "1.0.0", command("2.0.0", false),
		[]byte("pkg"), deps)

	if result.State != StateRolledBack {
		t.Fatalf("重启失败应回滚，state=%s reason=%s", result.State, result.Reason)
	}
	hasRollback := false
	for _, c := range inst.calls {
		if c == "rollback" {
			hasRollback = true
		}
	}
	if !hasRollback {
		t.Error("未执行 Rollback")
	}
}

func TestRun_UnhealthyRollsBack(t *testing.T) {
	t.Parallel()

	inst := &fakeInstaller{}
	deps, _ := happyDeps(inst)
	deps.Probe = &fakeProbe{err: errors.New("60s 内未上报心跳")}

	result := Run(context.Background(), "u-job", "1.0.0", command("2.0.0", false),
		[]byte("pkg"), deps)
	if result.State != StateRolledBack {
		t.Fatalf("健康失败应回滚: %s", result.State)
	}
	want := []string{"stage", "activate", "restart", "rollback", "restart"}
	if len(inst.calls) != len(want) {
		t.Fatalf("调用序列 %v，期望 %v", inst.calls, want)
	}
	for i := range want {
		if inst.calls[i] != want[i] {
			t.Errorf("第 %d 步=%s 期望 %s，全序列 %v", i, inst.calls[i], want[i], inst.calls)
		}
	}
}

func TestRun_RollbackFailureFails(t *testing.T) {
	t.Parallel()

	inst := &fakeInstaller{rollbackErr: errors.New("disk dead")}
	deps, _ := happyDeps(inst)
	deps.Probe = &fakeProbe{err: errors.New("unhealthy")}

	result := Run(context.Background(), "u-job", "1.0.0", command("2.0.0", false),
		[]byte("pkg"), deps)
	if result.State != StateFailed {
		t.Fatalf("回滚失败应为 failed: %s", result.State)
	}
}
