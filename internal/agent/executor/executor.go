// Package executor 在 agent 本地以独立进程组执行 shell 任务，
// 把启动、输出块、结束作为事件流返回；超时或取消会杀掉整个进程组，不留孤儿。
package executor

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// ResultKind 是任务结束方式。
type ResultKind string

// 结束结果（对应 spec 的四种 agent 侧终态）。
const (
	ResultSucceeded ResultKind = "succeeded"
	ResultFailed    ResultKind = "failed"
	ResultTimeout   ResultKind = "timeout"
	ResultCanceled  ResultKind = "canceled"
)

// 输出流标识。
const (
	StreamStdout = "stdout"
	StreamStderr = "stderr"
)

// Spec 描述一次执行。
type Spec struct {
	Command    string
	WorkingDir string
	ExtraEnv   map[string]string
	Timeout    time.Duration
}

// EventKind 区分执行事件。
type EventKind int

const (
	EventStarted EventKind = iota
	EventOutput
	EventFinished
)

// Event 是执行过程中的一个事件。
type Event struct {
	Kind     EventKind
	Stream   string
	Data     []byte
	Result   ResultKind
	ExitCode int
	Message  string
}

// Executor 执行 shell 命令。
type Executor interface {
	Run(ctx context.Context, spec Spec) <-chan Event
}

// LocalExecutor 在本机 /bin/sh 下执行命令。
type LocalExecutor struct{}

// NewLocalExecutor 构造本地执行器。
func NewLocalExecutor() *LocalExecutor {
	return &LocalExecutor{}
}

// outputBufferSize 单条输出事件的读取块大小上限（1MB 截断由上层统计总量）。
const outputBufferSize = 64 * 1024

// Run 启动命令并返回事件 channel；channel 关闭即代表进程已回收。
// 父 ctx 取消 → ResultCanceled；spec.Timeout 到期 → ResultTimeout；
// 两者都会杀掉整个进程组。
func (e *LocalExecutor) Run(parent context.Context, spec Spec) <-chan Event {
	events := make(chan Event, 32)
	go e.run(parent, spec, events)
	return events
}

func (e *LocalExecutor) run(parent context.Context, spec Spec, events chan<- Event) {
	defer close(events)

	timedCtx, cancel := context.WithTimeout(parent, spec.Timeout)
	defer cancel()

	cmd := exec.CommandContext(timedCtx, "/bin/sh", "-c", spec.Command)
	if spec.WorkingDir != "" {
		cmd.Dir = spec.WorkingDir
	}
	cmd.Env = mergeEnv(spec.ExtraEnv)
	// 独立进程组：杀掉负 PGID 可连同 shell 派生的子进程一起终止。
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	// 用 cmd.Stdout/Stderr 挂 writer：Wait 会等待 exec 内部 copy 完成，
	// 避免 StdoutPipe + 手动读时「Wait 先关管道、极快命令丢输出」的竞态。
	cmd.Stdout = &eventWriter{stream: StreamStdout, events: events, ctx: timedCtx}
	cmd.Stderr = &eventWriter{stream: StreamStderr, events: events, ctx: timedCtx}

	cmd.Cancel = func() error {
		// PGID 即组首（shell）PID；负 PID 杀整个组，连带派生的子进程。
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if err != nil && !errors.Is(err, syscall.ESRCH) {
			return err
		}
		return nil
	}
	// 杀组后最多再等 5s 让 IO 与进程回收结束。
	cmd.WaitDelay = 5 * time.Second

	if err := cmd.Start(); err != nil {
		emitFailed(events, ResultFailed, -1, "启动命令失败: "+err.Error())
		return
	}
	events <- Event{Kind: EventStarted}

	// Wait 在命令退出且 stdout/stderr copy goroutine 完成后才返回，
	// 因此返回时全部输出事件已入队。
	waitErr := cmd.Wait()

	result, exitCode := classify(waitErr, parent, timedCtx)
	events <- Event{
		Kind:     EventFinished,
		Result:   result,
		ExitCode: exitCode,
	}
}

func mergeEnv(extra map[string]string) []string {
	env := make([]string, 0, len(extra)+16)
	env = append(env, os.Environ()...)
	for key, value := range extra {
		env = append(env, key+"="+value)
	}
	return env
}

func emitFailed(events chan<- Event, result ResultKind, exitCode int, message string) {
	events <- Event{Kind: EventStarted}
	events <- Event{Kind: EventFinished, Result: result, ExitCode: exitCode, Message: message}
}

// eventWriter 把进程输出按不超过 64KB 的原始块转成事件，不改变字节内容。
type eventWriter struct {
	stream string
	events chan<- Event
	ctx    context.Context
}

func (w *eventWriter) Write(p []byte) (int, error) {
	total := len(p)
	for len(p) > 0 {
		n := len(p)
		if n > outputBufferSize {
			n = outputBufferSize
		}
		chunk := append([]byte(nil), p[:n]...)
		select {
		case w.events <- Event{Kind: EventOutput, Stream: w.stream, Data: chunk}:
		case <-w.ctx.Done():
			// 取消/超时：放弃剩余输出，WaitDelay 保证进程被回收。
			return total, nil
		}
		p = p[n:]
	}
	return total, nil
}

func classify(waitErr error, parent, timedCtx context.Context) (ResultKind, int) {
	if parent.Err() != nil {
		return ResultCanceled, -1
	}
	if timedCtx.Err() == context.DeadlineExceeded {
		return ResultTimeout, -1
	}

	var exitErr *exec.ExitError
	if errors.As(waitErr, &exitErr) {
		return ResultFailed, exitErr.ExitCode()
	}
	if waitErr != nil {
		return ResultFailed, -1
	}
	return ResultSucceeded, 0
}
