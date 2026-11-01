// Package executor 在 agent 本地以独立进程组执行 shell 任务，
// 把启动、输出块、结束作为事件流返回；超时或取消会杀掉整个进程组，不留孤儿。
package executor

import (
	"bufio"
	"context"
	"errors"
	"io"
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

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		emitFailed(events, ResultFailed, -1, "创建 stdout 管道失败: "+err.Error())
		return
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		emitFailed(events, ResultFailed, -1, "创建 stderr 管道失败: "+err.Error())
		return
	}

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

	pipeDone := make(chan struct{}, 2)
	go pumpOutput(stdout, StreamStdout, events, timedCtx, pipeDone)
	go pumpOutput(stderr, StreamStderr, events, timedCtx, pipeDone)

	waitErr := cmd.Wait()
	// 等待两个 reader 结束，保证 Finished 之前全部输出事件已入队。
	<-pipeDone
	<-pipeDone

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

// pumpOutput 按原始块（最大 64KB）把输出转成事件，不改变字节内容。
func pumpOutput(r io.Reader, stream string, events chan<- Event,
	ctx context.Context, done chan<- struct{}) {
	defer func() { done <- struct{}{} }()

	reader := bufio.NewReaderSize(r, outputBufferSize)
	buf := make([]byte, outputBufferSize)
	for {
		n, err := reader.Read(buf)
		if n > 0 {
			chunk := append([]byte(nil), buf[:n]...)
			select {
			case events <- Event{Kind: EventOutput, Stream: stream, Data: chunk}:
			case <-ctx.Done():
				return
			}
		}
		if err != nil {
			return
		}
	}
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
