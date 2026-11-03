package store

import (
	"context"
	"testing"
	"time"
)

func seedAgent(t *testing.T, s *Store, ctx context.Context, id, status string, hb time.Time) {
	t.Helper()
	a := Agent{
		AgentID: id, Hostname: id, Labels: map[string]string{},
		CertificateSerial: id, CreatedAt: hb, UpdatedAt: hb,
	}
	if err := s.InsertAgent(ctx, a); err != nil {
		t.Fatal(err)
	}
	if status != StatusOffline {
		if err := s.SetAgentStatus(ctx, id, status, hb); err != nil {
			t.Fatal(err)
		}
	}
}

func TestResetOnlineAgents(t *testing.T) {
	t.Parallel()
	s := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	seedAgent(t, s, ctx, "a1", StatusOnline, now)
	seedAgent(t, s, ctx, "a2", StatusOnline, now)
	seedAgent(t, s, ctx, "a3", StatusOffline, now)

	n, err := s.ResetOnlineAgents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("应重置 2 个 online，实际 %d", n)
	}
	if got, _ := s.GetAgent(ctx, "a1"); got.Status != StatusOffline {
		t.Errorf("a1 应 offline，实际 %s", got.Status)
	}
	if got, _ := s.GetAgent(ctx, "a3"); got.Status != StatusOffline {
		t.Errorf("a3 保持 offline")
	}
}

func TestPurgeOldData(t *testing.T) {
	t.Parallel()
	s := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	seedAgent(t, s, ctx, "ag", StatusOnline, now)

	// old：90 天前终态；new：最近终态；running：在途（永不删）
	mkFinished := func(id string, at time.Time) {
		j := Job{
			JobID: id, AgentID: "ag", Kind: KindCommand,
			Command: "x", RequestedBy: "t", CreatedAt: at, TimeoutSeconds: 300,
		}
		if err := s.CreateJob(ctx, j); err != nil {
			t.Fatal(err)
		}
		code := 0
		if _, err := s.UpdateJobState(ctx, id, JobStatusSucceeded, &code, false, "", at); err != nil {
			t.Fatal(err)
		}
	}
	old := now.Add(-90 * 24 * time.Hour)
	recent := now.Add(-time.Hour)
	mkFinished("old", old)
	mkFinished("new", recent)
	runJob := Job{
		JobID: "run", AgentID: "ag", Kind: KindCommand,
		Command: "x", RequestedBy: "t", CreatedAt: recent, TimeoutSeconds: 300,
		Status: JobStatusRunning,
	}
	if err := s.CreateJob(ctx, runJob); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendJobOutput(ctx, "old", JobOutputChunk{
		Seq: 1, Stream: StreamStdout, Data: []byte("out"),
	}); err != nil {
		t.Fatal(err)
	}

	// 审计：一条老、一条新
	if err := s.InsertAudit(ctx, AuditLog{At: old, Actor: "a", Action: "x"}); err != nil {
		t.Fatal(err)
	}
	if err := s.InsertAudit(ctx, AuditLog{At: recent, Actor: "a", Action: "y"}); err != nil {
		t.Fatal(err)
	}

	// 保留 30 天 job、60 天审计
	jobs, audits, err := s.PurgeOldData(ctx, now.Add(-30*24*time.Hour), now.Add(-60*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if jobs != 1 {
		t.Errorf("应删 1 个老 job，实际 %d", jobs)
	}
	if audits != 1 {
		t.Errorf("应删 1 条老审计，实际 %d", audits)
	}
	if _, err := s.GetJob(ctx, "old"); err == nil {
		t.Error("老 job 应被删除")
	}
	if size, _ := s.SumJobOutputSize(ctx, "old"); size != 0 {
		t.Errorf("老 job 输出应级联删除，size=%d", size)
	}
	if _, err := s.GetJob(ctx, "new"); err != nil {
		t.Error("新 job 应保留")
	}
	if _, err := s.GetJob(ctx, "run"); err != nil {
		t.Error("在途 running job 必须保留")
	}
}
