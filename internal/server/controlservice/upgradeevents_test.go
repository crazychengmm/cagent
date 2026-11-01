package controlservice

import (
	"context"
	"testing"
	"time"

	"github.com/crazychengmm/cagent/internal/server/store"
	pb "github.com/crazychengmm/cagent/proto/cagent/v1"
)

func seedUpgradeJob(t *testing.T, st *store.Store, agentID, jobID, version string) {
	t.Helper()
	now := time.Now().UTC()
	if err := st.InsertAgent(context.Background(), store.Agent{
		AgentID: agentID, Hostname: agentID, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateJob(context.Background(), store.Job{
		JobID: jobID, AgentID: agentID, Kind: store.KindUpgrade,
		Status: store.JobStatusPending, RequestedBy: "alice",
		ReleaseVersion: version, CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
}

func upgradeEvent(jobID string, status pb.UpgradeEvent_Status, msg string) *pb.UpgradeEvent {
	return &pb.UpgradeEvent{
		JobId:       jobID,
		Status:      status,
		Message:     msg,
		FromVersion: "1.0.0",
		ToVersion:   "2.0.0",
	}
}

func TestHandleUpgradeEvent_Lifecycle(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, st := newJobEventService(t)
	seedUpgradeJob(t, st, "ag-u", "u-1", "2.0.0")

	if err := svc.HandleUpgradeEvent(ctx,
		upgradeEvent("u-1", pb.UpgradeEvent_STATUS_INSTALLING, "重启中")); err != nil {
		t.Fatal(err)
	}
	job, _ := st.GetJob(ctx, "u-1")
	if job.Status != store.JobStatusRunning || job.StartedAt == nil {
		t.Errorf("INSTALLING 后应 running: %+v", job)
	}

	if err := svc.HandleUpgradeEvent(ctx,
		upgradeEvent("u-1", pb.UpgradeEvent_STATUS_SUCCEEDED, "ok")); err != nil {
		t.Fatal(err)
	}
	job, _ = st.GetJob(ctx, "u-1")
	if job.Status != store.JobStatusSucceeded || job.ExitCode == nil || *job.ExitCode != 0 {
		t.Errorf("SUCCEEDED 状态错误: %+v", job)
	}

	// 终态后重复事件无效。
	if err := svc.HandleUpgradeEvent(ctx,
		upgradeEvent("u-1", pb.UpgradeEvent_STATUS_FAILED, "late")); err != nil {
		t.Fatal(err)
	}
	job, _ = st.GetJob(ctx, "u-1")
	if job.Status != store.JobStatusSucceeded {
		t.Errorf("终态被覆盖: %s", job.Status)
	}
}

func TestHandleUpgradeEvent_RolledBackAndFailed(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, st := newJobEventService(t)
	seedUpgradeJob(t, st, "ag-u2", "u-2", "2.0.0")
	seedUpgradeJob(t, st, "ag-u3", "u-3", "3.0.0")

	if err := svc.HandleUpgradeEvent(ctx,
		upgradeEvent("u-2", pb.UpgradeEvent_STATUS_ROLLED_BACK, "健康失败")); err != nil {
		t.Fatal(err)
	}
	job, _ := st.GetJob(ctx, "u-2")
	if job.Status != store.JobStatusFailed {
		t.Errorf("回滚应记为失败: %s", job.Status)
	}

	if err := svc.HandleUpgradeEvent(ctx,
		upgradeEvent("u-3", pb.UpgradeEvent_STATUS_FAILED, "验签失败")); err != nil {
		t.Fatal(err)
	}
	job, _ = st.GetJob(ctx, "u-3")
	if job.Status != store.JobStatusFailed {
		t.Errorf("FAILED 应失败: %s", job.Status)
	}

	logs, _ := st.ListAudits(ctx, 10)
	var finishes int
	for _, l := range logs {
		if l.Action == store.ActionJobFinish {
			finishes++
		}
	}
	if finishes != 2 {
		t.Errorf("应 2 条完成审计，实际 %d", finishes)
	}
}

func TestHandleUpgradeEvent_UnknownIgnored(t *testing.T) {
	t.Parallel()

	svc, _ := newJobEventService(t)
	if err := svc.HandleUpgradeEvent(context.Background(),
		upgradeEvent("ghost", pb.UpgradeEvent_STATUS_FAILED, "x")); err != nil {
		t.Errorf("未知 job 应忽略: %v", err)
	}
}
