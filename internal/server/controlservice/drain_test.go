package controlservice

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/crazychengmm/cagent/internal/agent/control"
	"github.com/crazychengmm/cagent/internal/server/store"
	pb "github.com/crazychengmm/cagent/proto/cagent/v1"
)

type collectingHandler struct {
	ch chan *pb.JobDispatch
}

func (h *collectingHandler) OnDispatch(_ context.Context, _ control.Sender, d *pb.JobDispatch) {
	h.ch <- d
}
func (h *collectingHandler) OnCancel(string) {}

func pendingJob(jobID, agentID string, createdAt time.Time) store.Job {
	return store.Job{
		JobID: jobID, AgentID: agentID, Kind: store.KindCommand, Command: "cmd-" + jobID,
		TimeoutSeconds: 60, RequestedBy: "alice",
		Status: store.JobStatusPending, CreatedAt: createdAt,
	}
}

func TestControlFlow_DrainsPendingJobsOnConnect(t *testing.T) {
	h := startControlHarness(t)
	defer h.stop()

	ident := h.provisionAgent(t, "ag-drain")
	old := time.Now().Add(-time.Hour)
	recent := time.Now().Add(-time.Minute)
	if err := h.store.CreateJob(context.Background(), pendingJob("j-old", "ag-drain", old)); err != nil {
		t.Fatal(err)
	}
	if err := h.store.CreateJob(context.Background(), pendingJob("j-new", "ag-drain", recent)); err != nil {
		t.Fatal(err)
	}

	handler := &collectingHandler{ch: make(chan *pb.JobDispatch, 8)}
	loop, err := control.New(control.Config{
		Identity:          ident,
		ServerAddr:        h.addr,
		DataDir:           t.TempDir(),
		Version:           "1.0",
		HeartbeatInterval: 50 * time.Millisecond,
		JobHandler:        handler,
		Logger:            slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = loop.Run(ctx) }()

	got := map[string]bool{}
	deadline := time.After(3 * time.Second)
	for len(got) < 2 {
		select {
		case d := <-handler.ch:
			got[d.JobId] = true
			if d.GetCommand() != "cmd-"+d.JobId {
				t.Errorf("补投命令内容错误: %+v", d)
			}
		case <-deadline:
			t.Fatalf("未收到全部补投，got=%v", got)
		}
	}
	if !got["j-old"] || !got["j-new"] {
		t.Errorf("补投任务错误: %v", got)
	}
}

func TestExpireOnce_OnlyStalePending(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	h := startControlHarness(t)
	defer h.stop()

	now := time.Now().UTC()
	jobs := []store.Job{
		pendingJob("stale", "ag-x", now.Add(-25*time.Hour)),
		pendingJob("fresh", "ag-x", now.Add(-time.Hour)),
	}
	// ag-x 先登记（外键）。
	if err := h.store.InsertAgent(ctx, store.Agent{
		AgentID: "ag-x", Hostname: "x", Labels: map[string]string{},
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	running := pendingJob("running", "ag-x", now.Add(-25*time.Hour))
	running.Status = store.JobStatusRunning
	jobs = append(jobs, running)
	for _, j := range jobs {
		if err := h.store.CreateJob(ctx, j); err != nil {
			t.Fatal(err)
		}
	}

	n, err := ExpireOnce(ctx, h.store, 24*time.Hour, now)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("过期数量 = %d, 期望 1", n)
	}
	stale, _ := h.store.GetJob(ctx, "stale")
	if stale.Status != store.JobStatusExpired || stale.FinishedAt == nil {
		t.Errorf("stale 应 expired: %+v", stale)
	}
	fresh, _ := h.store.GetJob(ctx, "fresh")
	if fresh.Status != store.JobStatusPending {
		t.Errorf("fresh 应保持 pending，实际 %s", fresh.Status)
	}
	runningJob, _ := h.store.GetJob(ctx, "running")
	if runningJob.Status != store.JobStatusRunning {
		t.Errorf("running 不应被动到，实际 %s", runningJob.Status)
	}
}
