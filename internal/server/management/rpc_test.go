package management

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/crazychengmm/cagent/internal/server/dispatch"
	"github.com/crazychengmm/cagent/internal/server/enrollment"
	"github.com/crazychengmm/cagent/internal/server/store"
	pb "github.com/crazychengmm/cagent/proto/cagent/v1"
)

type fakeJobCreator struct {
	lastReq dispatch.CreateRequest
}

func (f *fakeJobCreator) CreateJobs(_ context.Context,
	req dispatch.CreateRequest) (*dispatch.CreateResult, error) {
	f.lastReq = req
	return &dispatch.CreateResult{JobIDs: []string{"job_a", "job_b"}}, nil
}

func newRPCService(t *testing.T, online OnlineDispatcher,
	tokens TokenService, jobs JobCreator) (*Service, *store.Store) {
	t.Helper()
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	svc, err := New(db, t.TempDir(), nil, online, tokens, jobs, time.Now,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return svc, db
}

func TestRPC_TokenLifecycle(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	tokens := enrollment.NewTokenService(db, 7*24*time.Hour, time.Now)
	svc, err := New(db, t.TempDir(), nil, nil, tokens, nil, time.Now,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}

	created, err := svc.CreateToken(ctx, &pb.CreateTokenRequest{TtlSeconds: 3600, Description: "ci"})
	if err != nil {
		t.Fatal(err)
	}
	if created.Plaintext == "" || created.Token.Id == "" {
		t.Errorf("创建响应错误: %+v", created)
	}
	tokenID := created.Token.Id
	plaintext := created.Plaintext

	listed, err := svc.ListTokens(ctx, &pb.ListTokensRequest{})
	if err != nil || len(listed.Tokens) != 1 || listed.Tokens[0].Description != "ci" {
		t.Errorf("列表错误: %+v err=%v", listed, err)
	}

	if _, err := svc.RevokeToken(ctx, &pb.RevokeTokenRequest{Id: tokenID}); err != nil {
		t.Fatal(err)
	}
	record, _ := db.GetEnrollmentTokenByPlaintext(context.Background(), plaintext)
	if record == nil || record.RevokedAt == nil {
		t.Error("吊销未生效")
	}
	if _, err := svc.RevokeToken(ctx, &pb.RevokeTokenRequest{Id: "missing"}); err == nil {
		t.Error("吊销不存在的 token 应报错")
	}
}

func TestRPC_AgentRead(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, db := newRPCService(t, nil, nil, nil)
	now := time.Now().UTC()
	db.InsertAgent(ctx, store.Agent{
		AgentID: "ag-1", Hostname: "h1", Version: "1.0",
		Labels: map[string]string{"env": "prod"}, CreatedAt: now, UpdatedAt: now,
	})

	listed, err := svc.ListAgents(ctx, &pb.ListAgentsRequest{})
	if err != nil || len(listed.Agents) != 1 || listed.Agents[0].Hostname != "h1" {
		t.Fatalf("列表错误: %v %v", listed, err)
	}
	shown, err := svc.ShowAgent(ctx, &pb.ShowAgentRequest{Id: "ag-1"})
	if err != nil || shown.Agent.Labels["env"] != "prod" {
		t.Fatalf("详情错误: %v", err)
	}
	if _, err := svc.ShowAgent(ctx, &pb.ShowAgentRequest{Id: "ghost"}); err == nil {
		t.Error("未知 agent 应报错")
	}
}

func TestRPC_RunJob(t *testing.T) {
	t.Parallel()

	creator := &fakeJobCreator{}
	svc, _ := newRPCService(t, nil, nil, creator)
	resp, err := svc.RunJob(context.Background(), &pb.RunJobRequest{
		Selector:       map[string]string{"env": "prod"},
		Command:        "uptime",
		TimeoutSeconds: 60,
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Matched != 2 || len(resp.JobIds) != 2 {
		t.Errorf("响应错误: %+v", resp)
	}
	if creator.lastReq.Command != "uptime" || creator.lastReq.Selector["env"] != "prod" ||
		creator.lastReq.Timeout != time.Minute {
		t.Errorf("请求传递错误: %+v", creator.lastReq)
	}
}

func TestRPC_JobReadAndCancel(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	online := &recordingOnline{ids: []string{"ag-1"}}
	svc, db := newRPCService(t, online, nil, nil)
	now := time.Now().UTC()
	db.InsertAgent(ctx, store.Agent{AgentID: "ag-1", Hostname: "h", CreatedAt: now, UpdatedAt: now})
	db.CreateJob(ctx, store.Job{
		JobID: "job-1", AgentID: "ag-1", Command: "uptime",
		Status: store.JobStatusRunning, RequestedBy: "admin", CreatedAt: now,
	})
	db.AppendJobOutput(ctx, "job-1", store.JobOutputChunk{Seq: 1, Stream: "stdout", Data: []byte("hi\n")})

	listed, err := svc.ListJobs(ctx, &pb.ListJobsRequest{Limit: 10})
	if err != nil || len(listed.Jobs) != 1 {
		t.Fatalf("列表错误: %v", err)
	}
	shown, err := svc.ShowJob(ctx, &pb.ShowJobRequest{Id: "job-1"})
	if err != nil {
		t.Fatal(err)
	}
	if shown.Job.Status != "running" || len(shown.Output) != 1 || string(shown.Output[0].Data) != "hi\n" {
		t.Errorf("详情/输出错误: %+v", shown)
	}

	if _, err := svc.CancelJob(ctx, &pb.CancelJobRequest{Id: "job-1"}); err != nil {
		t.Fatalf("取消: %v", err)
	}
	if got := online.cancelFor("ag-1"); got != "job-1" {
		t.Errorf("取消消息 job=%q", got)
	}

	// 离线 agent
	online.ids = nil
	online.cancels = nil
	if _, err := svc.CancelJob(ctx, &pb.CancelJobRequest{Id: "job-1"}); err == nil {
		t.Error("离线 agent 取消应报错")
	}
	if _, err := svc.CancelJob(ctx, &pb.CancelJobRequest{Id: "missing"}); err == nil {
		t.Error("未知 job 取消应报错")
	}
}
