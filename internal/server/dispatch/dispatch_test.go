package dispatch

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/crazychengmm/cagent/internal/server/store"
	pb "github.com/crazychengmm/cagent/proto/cagent/v1"
)

func TestMatchLabels(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		labels   map[string]string
		selector map[string]string
		want     bool
	}{
		{"空选择器匹配所有", map[string]string{"env": "prod"}, nil, true},
		{"单键匹配", map[string]string{"env": "prod", "role": "db"}, map[string]string{"env": "prod"}, true},
		{"AND 全满足", map[string]string{"env": "prod", "role": "db"}, map[string]string{"env": "prod", "role": "db"}, true},
		{"一键不满足", map[string]string{"env": "prod"}, map[string]string{"env": "prod", "role": "db"}, false},
		{"值不同", map[string]string{"env": "test"}, map[string]string{"env": "prod"}, false},
		{"被选机缺键", map[string]string{}, map[string]string{"env": "prod"}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := MatchLabels(tc.labels, tc.selector); got != tc.want {
				t.Errorf("MatchLabels() = %v, want %v", got, tc.want)
			}
		})
	}
}

type fakeOnline struct {
	mu       sync.Mutex
	ids      []string
	received map[string][]*pb.JobDispatch
}

func (f *fakeOnline) OnlineAgentIDs() []string { return f.ids }
func (f *fakeOnline) SendToAgent(agentID string, msg *pb.ServerMessage) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if d := msg.GetJobDispatch(); d != nil {
		f.received[agentID] = append(f.received[agentID], d)
	}
	return nil
}

func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func seedAgent(t *testing.T, st *store.Store, id string, labels map[string]string) {
	t.Helper()
	now := time.Now().UTC()
	if err := st.InsertAgent(context.Background(), store.Agent{
		AgentID: id, Hostname: id, Labels: labels, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestCreateJobs_MatchDispatchAndPending(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	st := newTestStore(t)
	seedAgent(t, st, "db1", map[string]string{"env": "prod", "role": "db"})
	seedAgent(t, st, "web1", map[string]string{"env": "prod", "role": "web"})
	seedAgent(t, st, "ci1", map[string]string{"env": "test", "role": "ci"})

	online := &fakeOnline{ids: []string{"db1"}, received: map[string][]*pb.JobDispatch{}}
	svc := New(st, online, time.Now, slog.New(slog.NewTextHandler(io.Discard, nil)))

	result, err := svc.CreateJobs(ctx, CreateRequest{
		Selector:    map[string]string{"env": "prod"},
		Command:     "uptime",
		RequestedBy: "alice",
	})
	if err != nil {
		t.Fatalf("CreateJobs: %v", err)
	}
	if len(result.JobIDs) != 2 {
		t.Fatalf("应创建 2 个 job，实际 %d", len(result.JobIDs))
	}

	jobs, _ := st.ListJobs(ctx, store.ListJobsFilter{})
	if len(jobs) != 2 {
		t.Fatalf("库中 job 数 = %d", len(jobs))
	}
	for _, job := range jobs {
		if job.Status != store.JobStatusPending || job.Command != "uptime" ||
			job.TimeoutSeconds != 300 || job.RequestedBy != "alice" {
			t.Errorf("job 默认值不符: %+v", job)
		}
	}

	if len(online.received["db1"]) != 1 {
		t.Errorf("在线机 db1 应收到 1 条 dispatch，实际 %d", len(online.received["db1"]))
	}
	if len(online.received["web1"]) != 0 {
		t.Error("离线机 web1 不应即时下发")
	}

	logs, _ := st.ListAudits(ctx, 10)
	var createAudit bool
	for _, l := range logs {
		if l.Action == store.ActionJobCreate && l.Actor == "alice" {
			createAudit = true
		}
	}
	if !createAudit {
		t.Error("缺少 job.create 审计")
	}
}

func TestCreateJobs_EmptySelectorMatchesAll(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)
	seedAgent(t, st, "a1", map[string]string{"env": "x"})
	seedAgent(t, st, "a2", nil)
	svc := New(st, &fakeOnline{received: map[string][]*pb.JobDispatch{}}, time.Now,
		slog.New(slog.NewTextHandler(io.Discard, nil)))

	result, err := svc.CreateJobs(context.Background(), CreateRequest{Command: "id"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.JobIDs) != 2 {
		t.Errorf("空选择器应匹配 2 台，实际 %d", len(result.JobIDs))
	}
}

func TestCreateJobs_NoMatch(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)
	seedAgent(t, st, "a1", map[string]string{"env": "test"})
	svc := New(st, nil, time.Now, slog.New(slog.NewTextHandler(io.Discard, nil)))

	if _, err := svc.CreateJobs(context.Background(), CreateRequest{
		Selector: map[string]string{"env": "prod"}, Command: "id",
	}); err != ErrNoMatch {
		t.Errorf("零匹配应返回 ErrNoMatch，实际 %v", err)
	}
}

func TestCreateJobs_Validation(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)
	seedAgent(t, st, "a1", map[string]string{"env": "test"})
	svc := New(st, nil, time.Now, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx := context.Background()

	if _, err := svc.CreateJobs(ctx, CreateRequest{Command: ""}); err == nil {
		t.Error("空命令应报错")
	}
	if _, err := svc.CreateJobs(ctx, CreateRequest{Command: "id", Timeout: 61 * time.Minute}); err == nil {
		t.Error("超时超过 60 分钟应报错")
	}
	if _, err := svc.CreateJobs(ctx, CreateRequest{Command: "id", Timeout: -time.Second}); err == nil {
		t.Error("负超时报错")
	}
}
