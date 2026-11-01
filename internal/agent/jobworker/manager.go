// Package jobworker 在 agent 本地维护固定并发的任务执行池：
// 派发入队、worker 执行、取消传播，并把执行事件经控制流回传 server。
package jobworker

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	agentaudit "github.com/crazychengmm/cagent/internal/agent/audit"
	"github.com/crazychengmm/cagent/internal/agent/control"
	"github.com/crazychengmm/cagent/internal/agent/executor"
	pb "github.com/crazychengmm/cagent/proto/cagent/v1"
)

// previewBuffer 只保留输出前 4KB 供本地审计摘要。
type previewBuffer struct {
	b strings.Builder
}

func (p *previewBuffer) Write(data []byte) {
	if p.b.Len() >= outputPreviewLimit {
		return
	}
	remaining := outputPreviewLimit - p.b.Len()
	if len(data) > remaining {
		data = data[:remaining]
	}
	p.b.Write(data)
}

func (p *previewBuffer) String() string { return p.b.String() }

const (
	defaultTimeout = 5 * time.Minute
	queueCapacity  = 1024
	// outputPreviewLimit 本地审计记录保留的输出前缀长度。
	outputPreviewLimit = 4096
)

// Auditor 是本地执行审计（由 audit.FileLogger 满足）。
type Auditor interface {
	JobStarted(e agentaudit.JobStart) error
	JobFinished(e agentaudit.JobFinish) error
}

type nopAuditor struct{}

func (nopAuditor) JobStarted(agentaudit.JobStart) error   { return nil }
func (nopAuditor) JobFinished(agentaudit.JobFinish) error { return nil }

// tracked 记录一个已接收任务的取消句柄与其排队期间是否已被取消。
type tracked struct {
	cancel   context.CancelFunc
	canceled bool
}

// Manager 实现 control.JobHandler，管理排队、并发与取消。
type Manager struct {
	exec    executor.Executor
	logger  *slog.Logger
	rootCtx context.Context

	queue   chan *queuedJob
	workers int

	mu      sync.Mutex
	running map[string]*tracked
	cache   *resultCache
	auditor Auditor
	wg      sync.WaitGroup
}

type queuedJob struct {
	dispatch *pb.JobDispatch
	sender   control.Sender
	ctx      context.Context
}

// NewManager 构造并发数为 concurrency 的执行池；auditor 为 nil 时不记录本地审计。
func NewManager(concurrency int, exec executor.Executor, logger *slog.Logger, auditor Auditor) *Manager {
	if auditor == nil {
		auditor = nopAuditor{}
	}
	return &Manager{
		exec:    exec,
		logger:  logger,
		workers: concurrency,
		queue:   make(chan *queuedJob, queueCapacity),
		running: make(map[string]*tracked),
		cache:   newResultCache(defaultResultCacheCapacity),
		auditor: auditor,
	}
}

// Start 启动 worker；ctx 是全部任务的生命周期根。
func (m *Manager) Start(ctx context.Context) {
	m.rootCtx = ctx
	for i := 0; i < m.workers; i++ {
		m.wg.Add(1)
		go func() {
			defer m.wg.Done()
			for {
				select {
				case job := <-m.queue:
					m.execute(job)
				case <-ctx.Done():
					return
				}
			}
		}()
	}
}

// Wait 等待全部 worker 退出。
func (m *Manager) Wait() {
	m.wg.Wait()
}

// ActiveCount 返回排队/在跑任务数（升级 grace 判定用，不含已完成缓存）。
func (m *Manager) ActiveCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.running)
}

// OnDispatch 实现 control.JobHandler：登记并入队。
// 控制连接断开不影响任务生命周期（绑定 Manager 的 rootCtx）。
func (m *Manager) OnDispatch(_ context.Context, sender control.Sender, d *pb.JobDispatch) {
	if d.GetJobId() == "" || d.GetCommand() == "" {
		m.logger.Warn("丢弃非法 dispatch（缺 job_id 或 command）")
		return
	}
	if m.rootCtx == nil {
		m.logger.Error("worker 池未 Start，丢弃任务", "job_id", d.GetJobId())
		return
	}
	if m.isRunning(d.GetJobId()) {
		m.logger.Warn("任务已在执行/排队，忽略重复派发", "job_id", d.GetJobId())
		return
	}
	// 已完成任务的重发：直接回放终态，绝不重新执行（幂等）。
	if fin, ok := m.cache.get(d.GetJobId()); ok {
		m.logger.Info("命中已完成结果缓存，直接回传不重复执行", "job_id", d.GetJobId())
		m.replay(d.GetJobId(), sender, fin)
		return
	}
	if !m.register(d.GetJobId()) {
		m.logger.Warn("并发登记失败，忽略", "job_id", d.GetJobId())
		return
	}

	jobCtx, cancel := context.WithCancel(m.rootCtx)
	m.setCancel(d.GetJobId(), cancel)
	m.queue <- &queuedJob{dispatch: d, sender: sender, ctx: jobCtx}
}

func (m *Manager) isRunning(jobID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.running[jobID]
	return ok
}

// replay 向新控制流回放缓存的 Started 与终态（输出已在 server 落库，无需重放）。
func (m *Manager) replay(jobID string, sender control.Sender, fin *pb.JobFinished) {
	m.sendEvent(sender, jobID, executor.Event{Kind: executor.EventStarted})
	m.sendEvent(sender, jobID, executor.Event{
		Kind:     executor.EventFinished,
		Result:   mapBack(fin.GetResult()),
		ExitCode: int(fin.GetExitCode()),
		Message:  fin.GetMessage(),
	})
}

func mapBack(r pb.JobFinished_Result) executor.ResultKind {
	switch r {
	case pb.JobFinished_RESULT_SUCCEEDED:
		return executor.ResultSucceeded
	case pb.JobFinished_RESULT_FAILED:
		return executor.ResultFailed
	case pb.JobFinished_RESULT_TIMEOUT:
		return executor.ResultTimeout
	case pb.JobFinished_RESULT_CANCELED:
		return executor.ResultCanceled
	default:
		return executor.ResultFailed
	}
}

// OnCancel 实现 control.JobHandler：取消排队/在跑任务（杀整个进程组）。
func (m *Manager) OnCancel(jobID string) {
	m.mu.Lock()
	tr := m.running[jobID]
	if tr != nil {
		tr.canceled = true
		if tr.cancel != nil {
			tr.cancel()
		}
	}
	m.mu.Unlock()

	if tr != nil {
		m.logger.Info("收到取消，终止任务", "job_id", jobID)
	}
}

func (m *Manager) register(jobID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.running[jobID]; exists {
		return false
	}
	m.running[jobID] = &tracked{}
	return true
}

func (m *Manager) setCancel(jobID string, cancel context.CancelFunc) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if tr, ok := m.running[jobID]; ok {
		tr.cancel = cancel
	}
}

func (m *Manager) execute(job *queuedJob) {
	d := job.dispatch
	defer func() {
		m.mu.Lock()
		delete(m.running, d.GetJobId())
		m.mu.Unlock()
	}()

	// 排队期间已被取消：不启动进程，直接回 CANCELED。
	m.mu.Lock()
	canceledBeforeStart := m.running[d.GetJobId()] != nil && m.running[d.GetJobId()].canceled
	m.mu.Unlock()
	if canceledBeforeStart {
		m.sendEvent(job.sender, d.GetJobId(), executor.Event{Kind: executor.EventStarted})
		m.auditStart(d)
		m.sendEvent(job.sender, d.GetJobId(), executor.Event{
			Kind: executor.EventFinished, Result: executor.ResultCanceled, ExitCode: -1,
		})
		m.auditFinish(d, executor.Event{Result: executor.ResultCanceled, ExitCode: -1}, "")
		return
	}

	timeout := defaultTimeout
	if d.GetTimeoutSeconds() > 0 {
		timeout = time.Duration(d.GetTimeoutSeconds()) * time.Second
	}
	spec := executor.Spec{
		Command:    d.GetCommand(),
		WorkingDir: d.GetWorkingDir(),
		ExtraEnv:   d.GetExtraEnv(),
		Timeout:    timeout,
	}

	var preview previewBuffer
	var final executor.Event
	m.auditStart(d)
	for ev := range m.exec.Run(job.ctx, spec) {
		if ev.Kind == executor.EventOutput {
			preview.Write(ev.Data)
		}
		m.sendEvent(job.sender, d.GetJobId(), ev)
		if ev.Kind == executor.EventFinished {
			final = ev
			// 缓存终态供重发幂等；输出不缓存（server 已落库）。
			m.cache.put(d.GetJobId(), &pb.JobFinished{
				Result:   mapResult(ev.Result),
				ExitCode: int32(ev.ExitCode),
				Message:  ev.Message,
			})
		}
	}
	m.auditFinish(d, final, preview.String())
}

func (m *Manager) auditStart(d *pb.JobDispatch) {
	if err := m.auditor.JobStarted(agentaudit.JobStart{
		TS:          time.Now().UTC(),
		JobID:       d.GetJobId(),
		RequestedBy: d.GetRequestedBy(),
		Command:     d.GetCommand(),
		WorkingDir:  d.GetWorkingDir(),
	}); err != nil {
		m.logger.Warn("写本地开始审计失败", "job_id", d.GetJobId(), "error", err.Error())
	}
}

func (m *Manager) auditFinish(d *pb.JobDispatch, ev executor.Event, preview string) {
	if err := m.auditor.JobFinished(agentaudit.JobFinish{
		TS:            time.Now().UTC(),
		JobID:         d.GetJobId(),
		Result:        string(ev.Result),
		ExitCode:      ev.ExitCode,
		OutputPreview: preview,
	}); err != nil {
		m.logger.Warn("写本地结束审计失败", "job_id", d.GetJobId(), "error", err.Error())
	}
}

// sendEvent 把执行器事件映射为 proto JobEvent 并回传。
func (m *Manager) sendEvent(sender control.Sender, jobID string, ev executor.Event) {
	msg := &pb.AgentMessage{}
	switch ev.Kind {
	case executor.EventStarted:
		msg.Payload = &pb.AgentMessage_JobEvent{
			JobEvent: &pb.JobEvent{JobId: jobID, Event: &pb.JobEvent_Started{Started: &pb.JobStarted{}}},
		}
	case executor.EventOutput:
		stream := pb.JobOutput_STREAM_STDOUT
		if ev.Stream == executor.StreamStderr {
			stream = pb.JobOutput_STREAM_STDERR
		}
		msg.Payload = &pb.AgentMessage_JobEvent{
			JobEvent: &pb.JobEvent{JobId: jobID, Event: &pb.JobEvent_Output{
				Output: &pb.JobOutput{Stream: stream, Data: ev.Data},
			}},
		}
	case executor.EventFinished:
		msg.Payload = &pb.AgentMessage_JobEvent{
			JobEvent: &pb.JobEvent{JobId: jobID, Event: &pb.JobEvent_Finished{
				Finished: &pb.JobFinished{
					Result:   mapResult(ev.Result),
					ExitCode: int32(ev.ExitCode),
					Message:  ev.Message,
				},
			}},
		}
	default:
		return
	}
	if err := sender.Send(msg); err != nil {
		// 连接已断：结果重传由幂等结果缓存处理（任务 5.4）。
		m.logger.Warn("回传 JobEvent 失败", "job_id", jobID, "error", err.Error())
	}
}

func mapResult(r executor.ResultKind) pb.JobFinished_Result {
	switch r {
	case executor.ResultSucceeded:
		return pb.JobFinished_RESULT_SUCCEEDED
	case executor.ResultFailed:
		return pb.JobFinished_RESULT_FAILED
	case executor.ResultTimeout:
		return pb.JobFinished_RESULT_TIMEOUT
	case executor.ResultCanceled:
		return pb.JobFinished_RESULT_CANCELED
	default:
		return pb.JobFinished_RESULT_UNSPECIFIED
	}
}
