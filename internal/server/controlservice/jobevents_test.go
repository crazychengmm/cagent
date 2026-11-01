package controlservice

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/crazychengmm/cagent/internal/server/ca"
	"github.com/crazychengmm/cagent/internal/server/store"
	pb "github.com/crazychengmm/cagent/proto/cagent/v1"
)

func newJobEventService(t *testing.T) (*Service, *store.Store) {
	t.Helper()
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	rootCA, err := ca.LoadOrCreate(filepath.Join(t.TempDir(), "ca"))
	if err != nil {
		t.Fatal(err)
	}
	return New(db, rootCA, time.Hour, time.Now,
		slog.New(slog.NewTextHandler(io.Discard, nil))), db
}

func createJobRow(t *testing.T, db *store.Store, agentID, jobID string) {
	t.Helper()
	now := time.Now().UTC()
	if err := db.InsertAgent(context.Background(), store.Agent{
		AgentID: agentID, Hostname: "h", Labels: map[string]string{},
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateJob(context.Background(), store.Job{
		JobID: jobID, AgentID: agentID, Kind: store.KindCommand,
		Command: "uptime", TimeoutSeconds: 60, RequestedBy: "alice",
		Status: store.JobStatusPending, CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestHandleJobEvent_Lifecycle(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, db := newJobEventService(t)
	createJobRow(t, db, "ag-1", "j-1")

	if err := svc.HandleJobEvent(ctx, &pb.JobEvent{
		JobId: "j-1", Event: &pb.JobEvent_Started{Started: &pb.JobStarted{}},
	}); err != nil {
		t.Fatal(err)
	}
	got, _ := db.GetJob(ctx, "j-1")
	if got.Status != store.JobStatusRunning || got.StartedAt == nil {
		t.Errorf("Started 后状态不符: %+v", got)
	}

	outputs := []struct {
		stream pb.JobOutput_Stream
		data   string
	}{
		{pb.JobOutput_STREAM_STDOUT, "out1\n"},
		{pb.JobOutput_STREAM_STDERR, "err1\n"},
		{pb.JobOutput_STREAM_STDOUT, "out2"},
	}
	for _, o := range outputs {
		if err := svc.HandleJobEvent(ctx, &pb.JobEvent{
			JobId: "j-1",
			Event: &pb.JobEvent_Output{Output: &pb.JobOutput{Stream: o.stream, Data: []byte(o.data)}},
		}); err != nil {
			t.Fatal(err)
		}
	}
	chunks, err := db.GetJobOutput(ctx, "j-1")
	if err != nil || len(chunks) != 3 {
		t.Fatalf("输出块 = %d, %v", len(chunks), err)
	}
	if chunks[0].Seq != 1 || chunks[1].Stream != store.StreamStderr || string(chunks[2].Data) != "out2" {
		t.Errorf("输出顺序/内容错误: %+v", chunks)
	}
	size, _ := db.SumJobOutputSize(ctx, "j-1")
	if size != int64(len("out1\n")+len("err1\n")+len("out2")) {
		t.Errorf("输出总大小 = %d", size)
	}

	exit := 0
	if err := svc.HandleJobEvent(ctx, &pb.JobEvent{
		JobId: "j-1",
		Event: &pb.JobEvent_Finished{Finished: &pb.JobFinished{
			Result: pb.JobFinished_RESULT_SUCCEEDED, ExitCode: int32(exit),
		}},
	}); err != nil {
		t.Fatal(err)
	}
	got, _ = db.GetJob(ctx, "j-1")
	if got.Status != store.JobStatusSucceeded || got.ExitCode == nil || *got.ExitCode != 0 || got.FinishedAt == nil {
		t.Errorf("终态记录错误: %+v", got)
	}

	logs, _ := db.ListAudits(ctx, 10)
	var hasFinishAudit bool
	for _, l := range logs {
		if l.Action == store.ActionJobFinish && l.JobID == "j-1" {
			hasFinishAudit = true
		}
	}
	if !hasFinishAudit {
		t.Error("缺少 job.finish 审计")
	}
}

func TestHandleJobEvent_FailedExitCode(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, db := newJobEventService(t)
	createJobRow(t, db, "ag-2", "j-2")

	_ = svc.HandleJobEvent(ctx, &pb.JobEvent{JobId: "j-2", Event: &pb.JobEvent_Started{Started: &pb.JobStarted{}}})
	err := svc.HandleJobEvent(ctx, &pb.JobEvent{
		JobId: "j-2",
		Event: &pb.JobEvent_Finished{Finished: &pb.JobFinished{
			Result: pb.JobFinished_RESULT_FAILED, ExitCode: 2, Message: "boom",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := db.GetJob(ctx, "j-2")
	if got.Status != store.JobStatusFailed || got.ExitCode == nil || *got.ExitCode != 2 || got.Message != "boom" {
		t.Errorf("失败终态错误: %+v", got)
	}
}

func TestHandleJobEvent_TerminalStateImmutable(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, db := newJobEventService(t)
	createJobRow(t, db, "ag-3", "j-3")

	_ = svc.HandleJobEvent(ctx, &pb.JobEvent{JobId: "j-3", Event: &pb.JobEvent_Started{Started: &pb.JobStarted{}}})
	_ = svc.HandleJobEvent(ctx, &pb.JobEvent{JobId: "j-3", Event: &pb.JobEvent_Finished{
		Finished: &pb.JobFinished{Result: pb.JobFinished_RESULT_CANCELED, ExitCode: -1},
	}})
	// 终态后重放 Started/Finished 均不得回退。
	_ = svc.HandleJobEvent(ctx, &pb.JobEvent{JobId: "j-3", Event: &pb.JobEvent_Started{Started: &pb.JobStarted{}}})
	_ = svc.HandleJobEvent(ctx, &pb.JobEvent{JobId: "j-3", Event: &pb.JobEvent_Finished{
		Finished: &pb.JobFinished{Result: pb.JobFinished_RESULT_SUCCEEDED, ExitCode: 0},
	}})

	got, _ := db.GetJob(ctx, "j-3")
	if got.Status != store.JobStatusCanceled {
		t.Errorf("终态被回退为 %s", got.Status)
	}
}

func TestHandleJobEvent_OutputTruncatedAt1MB(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, db := newJobEventService(t)
	createJobRow(t, db, "ag-4", "j-4")
	_ = svc.HandleJobEvent(ctx, &pb.JobEvent{JobId: "j-4", Event: &pb.JobEvent_Started{Started: &pb.JobStarted{}}})

	// 1200 块 × 2000 字节 = 2.4MB，服务端只应保留 1MB。
	block := make([]byte, 2000)
	for i := range block {
		block[i] = 'x'
	}
	for i := 0; i < 1200; i++ {
		if err := svc.HandleJobEvent(ctx, &pb.JobEvent{
			JobId: "j-4",
			Event: &pb.JobEvent_Output{Output: &pb.JobOutput{
				Stream: pb.JobOutput_STREAM_STDOUT, Data: block,
			}},
		}); err != nil {
			t.Fatal(err)
		}
	}

	size, _ := db.SumJobOutputSize(ctx, "j-4")
	if size != maxJobOutputBytes {
		t.Errorf("截断后总量 = %d, 期望 %d", size, maxJobOutputBytes)
	}
	_ = svc.HandleJobEvent(ctx, &pb.JobEvent{JobId: "j-4", Event: &pb.JobEvent_Finished{
		Finished: &pb.JobFinished{Result: pb.JobFinished_RESULT_SUCCEEDED, ExitCode: 0},
	}})
	got, _ := db.GetJob(ctx, "j-4")
	if !got.Truncated {
		t.Error("超过 1MB 的 job 终态 truncated 应为 true")
	}
}

func TestHandleJobEvent_UnknownJobIgnored(t *testing.T) {
	t.Parallel()

	svc, _ := newJobEventService(t)
	err := svc.HandleJobEvent(context.Background(), &pb.JobEvent{
		JobId: "ghost", Event: &pb.JobEvent_Started{Started: &pb.JobStarted{}},
	})
	if err != nil {
		t.Errorf("未知 job 应被忽略而非返回错误: %v", err)
	}
}
