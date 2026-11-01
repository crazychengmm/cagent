package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestOpen_MigrationIdempotent(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "test.db")
	s1, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("首次 Open: %v", err)
	}
	s1.Close()

	s2, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("二次 Open 应幂等: %v", err)
	}
	if err := s2.Ping(context.Background()); err != nil {
		t.Fatalf("Ping: %v", err)
	}
	s2.Close()
}

func TestEnrollmentTokens(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := openTestStore(t)
	now := time.Now().UTC()

	tok := EnrollmentToken{
		ID: "et-1", CreatedBy: "admin", CreatedAt: now,
		ExpiresAt: now.Add(time.Hour), Description: "装机批 A",
	}
	const plaintext = "cagt_secret_token_value"
	if err := s.CreateEnrollmentToken(ctx, tok, plaintext); err != nil {
		t.Fatalf("CreateEnrollmentToken: %v", err)
	}

	got, err := s.GetEnrollmentTokenByPlaintext(ctx, plaintext)
	if err != nil {
		t.Fatalf("按明文反查: %v", err)
	}
	if got.ID != "et-1" {
		t.Errorf("ID = %q", got.ID)
	}

	if _, err := s.GetEnrollmentTokenByPlaintext(ctx, "wrong"); !errors.Is(err, ErrNotFound) {
		t.Errorf("错误明文应 ErrNotFound，实际 %v", err)
	}

	list, err := s.ListEnrollmentTokens(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("List = %v, %v", list, err)
	}

	ok, err := s.RevokeEnrollmentToken(ctx, "et-1", now)
	if err != nil || !ok {
		t.Errorf("首次吊销应成功，ok=%v err=%v", ok, err)
	}
	ok, err = s.RevokeEnrollmentToken(ctx, "et-1", now)
	if err != nil || ok {
		t.Errorf("重复吊销应返回 false，ok=%v err=%v", ok, err)
	}
	got, _ = s.GetEnrollmentTokenByPlaintext(ctx, plaintext)
	if got.RevokedAt == nil {
		t.Error("吊销后 revoked_at 应非空")
	}
}

func newTestAgent(t *testing.T, s *Store, id string) Agent {
	t.Helper()
	now := time.Now().UTC()
	a := Agent{
		AgentID: id, Hostname: "web-1", Labels: map[string]string{"env": "prod"},
		CertificateSerial: "01", CreatedAt: now, UpdatedAt: now,
	}
	if err := s.InsertAgent(context.Background(), a); err != nil {
		t.Fatalf("InsertAgent: %v", err)
	}
	return a
}

func TestAgentsLifecycle(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := openTestStore(t)
	newTestAgent(t, s, "ag-1")

	if _, err := s.GetAgent(ctx, "ag-1"); err != nil {
		t.Fatalf("GetAgent: %v", err)
	}
	if _, err := s.GetAgent(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("缺失 agent 应 ErrNotFound，实际 %v", err)
	}
	if err := s.InsertAgent(ctx, Agent{AgentID: "ag-1", CreatedAt: time.Now(), UpdatedAt: time.Now()}); err == nil {
		t.Error("重复 agent_id 应报错")
	}

	hb := time.Now().UTC()
	if err := s.RecordHeartbeat(ctx, "ag-1", "1.2.3", map[string]string{"env": "prod", "role": "db"}, hb); err != nil {
		t.Fatalf("RecordHeartbeat: %v", err)
	}
	got, _ := s.GetAgent(ctx, "ag-1")
	if got.Status != StatusOnline || got.Version != "1.2.3" || got.Labels["role"] != "db" ||
		got.LastHeartbeatAt == nil || !got.LastHeartbeatAt.Equal(hb) {
		t.Errorf("心跳刷新不符: %+v", got)
	}

	stale, err := s.ListAgentsOlderThan(ctx, hb.Add(time.Second))
	if err != nil {
		t.Fatalf("ListAgentsOlderThan: %v", err)
	}
	if len(stale) != 1 || stale[0].AgentID != "ag-1" {
		t.Errorf("应扫到 1 台超时 agent，实际 %d", len(stale))
	}
	fresh, err := s.ListAgentsOlderThan(ctx, hb.Add(-time.Second))
	if err != nil || len(fresh) != 0 {
		t.Errorf("cutoff 早于心跳不应扫到，实际 %d, %v", len(fresh), err)
	}
}

func TestJobsStateMachineAndOutput(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := openTestStore(t)
	newTestAgent(t, s, "ag-1")
	now := time.Now().UTC()

	// 外键：不存在的 agent 不能建 job。
	bad := Job{JobID: "j-bad", AgentID: "ghost", Status: JobStatusPending, Kind: KindCommand, CreatedAt: now}
	if err := s.CreateJob(ctx, bad); err == nil {
		t.Error("引用不存在 agent 的 job 应因外键失败")
	}

	job := Job{
		JobID: "j-1", AgentID: "ag-1", Kind: KindCommand, Command: "uptime",
		TimeoutSeconds: 300, RequestedBy: "alice",
		Status: JobStatusPending, ExtraEnv: map[string]string{"K": "V"}, CreatedAt: now,
	}
	if err := s.CreateJob(ctx, job); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	if err := s.CreateJob(ctx, job); err == nil {
		t.Error("重复 job_id 应报错（幂等基础）")
	}

	moved, err := s.UpdateJobState(ctx, "j-1", JobStatusRunning, nil, false, "", now.Add(time.Second))
	if err != nil || !moved {
		t.Fatalf("pending→running 应成功，moved=%v err=%v", moved, err)
	}
	code := 0
	moved, err = s.UpdateJobState(ctx, "j-1", JobStatusSucceeded, &code, false, "", now.Add(2*time.Second))
	if err != nil || !moved {
		t.Fatalf("running→succeeded 应成功，moved=%v err=%v", moved, err)
	}
	moved, err = s.UpdateJobState(ctx, "j-1", JobStatusRunning, nil, false, "", now.Add(3*time.Second))
	if err != nil || moved {
		t.Errorf("终态回退应返回 false，moved=%v err=%v", moved, err)
	}

	got, _ := s.GetJob(ctx, "j-1")
	if got.Status != JobStatusSucceeded || got.ExitCode == nil || *got.ExitCode != 0 ||
		got.StartedAt == nil || got.FinishedAt == nil {
		t.Errorf("终态记录不符: %+v", got)
	}

	if err := s.AppendJobOutput(ctx, "j-1", JobOutputChunk{Seq: 1, Stream: StreamStdout, Data: []byte("a")}); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendJobOutput(ctx, "j-1", JobOutputChunk{Seq: 2, Stream: StreamStderr, Data: []byte("bb")}); err != nil {
		t.Fatal(err)
	}
	size, err := s.SumJobOutputSize(ctx, "j-1")
	if err != nil || size != 3 {
		t.Errorf("输出总字节 = %d, 期望 3，err=%v", size, err)
	}
	chunks, err := s.GetJobOutput(ctx, "j-1")
	if err != nil || len(chunks) != 2 || string(chunks[1].Data) != "bb" {
		t.Errorf("输出块不符: %+v, %v", chunks, err)
	}

	// ON DELETE CASCADE：删 job 后输出随之消失。
	if _, err := s.db.ExecContext(ctx, `DELETE FROM jobs WHERE job_id = 'j-1'`); err != nil {
		t.Fatal(err)
	}
	chunks, _ = s.GetJobOutput(ctx, "j-1")
	if len(chunks) != 0 {
		t.Errorf("级联删除后不应有输出块，实际 %d", len(chunks))
	}
}

func TestAuditLogs(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := openTestStore(t)
	if err := s.InsertAudit(ctx, AuditLog{
		Actor: "alice", Action: ActionJobCreate, AgentID: "ag-1", JobID: "j-1",
		Detail: []byte(`{"command":"uptime"}`),
	}); err != nil {
		t.Fatalf("InsertAudit: %v", err)
	}
	if err := s.InsertAudit(ctx, AuditLog{Actor: "system", Action: ActionAgentEnroll}); err != nil {
		t.Fatalf("空关联审计: %v", err)
	}
	if err := s.InsertAudit(ctx, AuditLog{
		Actor: "a", Action: "x", Detail: []byte(`{bad`),
	}); err == nil {
		t.Error("非法 JSON detail 应报错")
	}

	logs, err := s.ListAudits(ctx, 10)
	if err != nil || len(logs) != 2 {
		t.Fatalf("ListAudits = %d, %v", len(logs), err)
	}
	if logs[0].Action != ActionAgentEnroll {
		t.Error("应按时间倒序，最新在前")
	}
}

func TestReleases(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := openTestStore(t)
	now := time.Now().UTC()
	r := Release{Version: "1.0.0", Size: 42, SHA256Hex: "abc", Signature: []byte("sig"), UploadedAt: now}
	if err := s.InsertRelease(ctx, r); err != nil {
		t.Fatalf("InsertRelease: %v", err)
	}
	if err := s.InsertRelease(ctx, r); err == nil {
		t.Error("重复版本应报错")
	}
	if _, err := s.GetRelease(ctx, "1.0.0"); err != nil {
		t.Errorf("GetRelease: %v", err)
	}
	if _, err := s.GetRelease(ctx, "9.9.9"); !errors.Is(err, ErrNotFound) {
		t.Errorf("缺失版本应 ErrNotFound，实际 %v", err)
	}
	list, err := s.ListReleases(ctx)
	if err != nil || len(list) != 1 {
		t.Errorf("ListReleases = %d, %v", len(list), err)
	}
}
