package executor

import (
	"bufio"
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func collect(t *testing.T, events <-chan Event) (ResultKind, int, string, string) {
	t.Helper()
	var stdout, stderr strings.Builder
	var result ResultKind
	var exitCode int
	finished := false
	for ev := range events {
		switch ev.Kind {
		case EventOutput:
			if ev.Stream == StreamStdout {
				stdout.Write(ev.Data)
			} else {
				stderr.Write(ev.Data)
			}
		case EventFinished:
			result = ev.Result
			exitCode = ev.ExitCode
			finished = true
		}
	}
	if !finished {
		t.Fatal("未收到 Finished 事件")
	}
	return result, exitCode, stdout.String(), stderr.String()
}

func TestRun_Success(t *testing.T) {
	t.Parallel()

	ex := NewLocalExecutor()
	result, code, stdout, _ := collect(t, ex.Run(context.Background(), Spec{
		Command: "printf hello", Timeout: 5 * time.Second,
	}))
	if result != ResultSucceeded || code != 0 {
		t.Errorf("result=%s code=%d", result, code)
	}
	if stdout != "hello" {
		t.Errorf("stdout=%q", stdout)
	}
}

func TestRun_NonZeroExit(t *testing.T) {
	t.Parallel()

	ex := NewLocalExecutor()
	result, code, _, _ := collect(t, ex.Run(context.Background(), Spec{
		Command: "exit 7", Timeout: 5 * time.Second,
	}))
	if result != ResultFailed || code != 7 {
		t.Errorf("result=%s code=%d, 期望 failed/7", result, code)
	}
}

func TestRun_Stderr(t *testing.T) {
	t.Parallel()

	ex := NewLocalExecutor()
	result, _, _, stderr := collect(t, ex.Run(context.Background(), Spec{
		Command: "echo oops >&2; exit 1", Timeout: 5 * time.Second,
	}))
	if result != ResultFailed {
		t.Errorf("result=%s", result)
	}
	if !strings.Contains(stderr, "oops") {
		t.Errorf("stderr=%q", stderr)
	}
}

func TestRun_WorkingDirAndEnv(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	ex := NewLocalExecutor()
	result, _, stdout, _ := collect(t, ex.Run(context.Background(), Spec{
		Command:    "pwd; printenv CAGENT_TEST_VAR",
		WorkingDir: dir,
		ExtraEnv:   map[string]string{"CAGENT_TEST_VAR": "value42"},
		Timeout:    5 * time.Second,
	}))
	if result != ResultSucceeded {
		t.Fatalf("result=%s", result)
	}
	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	if lines[0] != realDir || lines[1] != "value42" {
		t.Errorf("输出=%v, 期望 [%s value42]", lines, realDir)
	}
}

func TestRun_TimeoutKillsProcessGroup(t *testing.T) {
	t.Parallel()

	ex := NewLocalExecutor()
	start := time.Now()
	result, _, _, _ := collect(t, ex.Run(context.Background(), Spec{
		// 两个独立标记的长睡眠：一个后台、一个前台，同属一个进程组。
		Command: "sleep 876501 & sleep 876501", Timeout: 200 * time.Millisecond,
	}))
	elapsed := time.Since(start)
	if result != ResultTimeout {
		t.Errorf("result=%s, 期望 timeout", result)
	}
	if elapsed > 5*time.Second {
		t.Errorf("超时回收耗时 %s，超过 WaitDelay 预算", elapsed)
	}
	// 进程组（含后台子进程）不应残留。
	if pgrepMarker(t, "876501") {
		t.Error("超时后进程组仍有残留进程")
	}
}

func TestRun_ParentCancelKillsProcessGroup(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	ex := NewLocalExecutor()
	events := ex.Run(ctx, Spec{Command: "sleep 876502 & sleep 876502", Timeout: time.Minute})
	time.Sleep(200 * time.Millisecond)
	cancel()

	var result ResultKind
	for ev := range events {
		if ev.Kind == EventFinished {
			result = ev.Result
		}
	}
	if result != ResultCanceled {
		t.Errorf("result=%s, 期望 canceled", result)
	}
	if pgrepMarker(t, "876502") {
		t.Error("取消后进程组仍有残留进程")
	}
}

// pgrepMarker 检查系统中是否仍有命令行含 marker 的进程。
func pgrepMarker(t *testing.T, marker string) bool {
	t.Helper()
	out, err := exec.Command("pgrep", "-fl", marker).CombinedOutput()
	if err != nil {
		return false // pgrep 无匹配时退出码非 0
	}
	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	for scanner.Scan() {
		line := scanner.Text()
		if strings.Contains(line, marker) && !strings.Contains(line, "pgrep") {
			return true
		}
	}
	return false
}
