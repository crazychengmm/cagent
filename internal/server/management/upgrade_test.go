package management

import (
	"context"
	"crypto/ed25519"
	"io"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/crazychengmm/cagent/internal/server/store"
	pb "github.com/crazychengmm/cagent/proto/cagent/v1"
)

type fakeOnline struct {
	mu       sync.Mutex
	ids      []string
	upgrades map[string][]*pb.UpgradeCommand
}

func (f *fakeOnline) OnlineAgentIDs() []string { return f.ids }
func (f *fakeOnline) SendToAgent(agentID string, msg *pb.ServerMessage) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if u := msg.GetUpgradeCommand(); u != nil {
		f.upgrades[agentID] = append(f.upgrades[agentID], u)
	}
	return nil
}
func (f *fakeOnline) upgradesFor(id string) []*pb.UpgradeCommand {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.upgrades[id]
}

func seedAgentAndRelease(t *testing.T, st *store.Store, agentID string, labels map[string]string) {
	t.Helper()
	now := time.Now().UTC()
	if err := st.InsertAgent(context.Background(), store.Agent{
		AgentID: agentID, Hostname: agentID, Labels: labels,
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
}

func newManagementOnline(t *testing.T, pub ed25519.PublicKey, online OnlineDispatcher) (*Service, *store.Store) {
	t.Helper()
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	svc, err := New(db, t.TempDir(), pub, online, nil, nil, time.Now,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return svc, db
}

func TestUpgrade_CreatesJobsAndDispatchesOnline(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	online := &fakeOnline{ids: []string{"db1"}, upgrades: map[string][]*pb.UpgradeCommand{}}
	svc, st := newManagementOnline(t, nil, online)
	seedAgentAndRelease(t, st, "db1", map[string]string{"env": "prod", "role": "db"})
	seedAgentAndRelease(t, st, "web1", map[string]string{"env": "prod", "role": "web"})
	seedAgentAndRelease(t, st, "ci1", map[string]string{"env": "test"})
	now := time.Now().UTC()
	if err := st.InsertRelease(ctx, store.Release{
		Version: "2.0.0", Size: 42, SHA256Hex: "abc",
		Signature: []byte("sig"), UploadedAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	resp, err := svc.Upgrade(ctx, &pb.UpgradeRequest{
		Selector:       map[string]string{"env": "prod"},
		Version:        "2.0.0",
		AllowDowngrade: true,
	})
	if err != nil {
		t.Fatalf("Upgrade: %v", err)
	}
	if resp.Matched != 2 || len(resp.JobIds) != 2 {
		t.Fatalf("matched=%d jobs=%d", resp.Matched, len(resp.JobIds))
	}

	jobs, _ := st.ListJobs(ctx, store.ListJobsFilter{Kind: store.KindUpgrade})
	if len(jobs) != 2 {
		t.Fatalf("upgrade job 数 = %d", len(jobs))
	}
	for _, j := range jobs {
		if j.Status != store.JobStatusPending || j.ReleaseVersion != "2.0.0" ||
			j.SHA256Hex != "abc" || string(j.Signature) != "sig" || j.PackageSize != 42 ||
			!j.AllowDowngrade {
			t.Errorf("upgrade job 参数错误: %+v", j)
		}
	}

	got := online.upgradesFor("db1")
	if len(got) != 1 || got[0].GetVersion() != "2.0.0" || !got[0].GetAllowDowngrade() {
		t.Errorf("在线机升级指令错误: %+v", got)
	}
	if len(online.upgradesFor("web1")) != 0 {
		t.Error("离线机不应即时下发")
	}

	logs, _ := st.ListAudits(ctx, 10)
	var hasAudit bool
	for _, l := range logs {
		if l.Action == store.ActionUpgradeDispatch {
			hasAudit = true
		}
	}
	if !hasAudit {
		t.Error("缺少 upgrade.dispatch 审计")
	}
}

func TestUpgrade_VersionNotPublished(t *testing.T) {
	t.Parallel()

	svc, st := newManagementOnline(t, nil, &fakeOnline{})
	seedAgentAndRelease(t, st, "a1", nil)
	_, err := svc.Upgrade(context.Background(), &pb.UpgradeRequest{Version: "9.9.9"})
	if status.Code(err) != codes.FailedPrecondition {
		t.Errorf("未发布版本应 FailedPrecondition，实际 %v", err)
	}
}

func TestUpgrade_NoMatch(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, st := newManagementOnline(t, nil, &fakeOnline{})
	seedAgentAndRelease(t, st, "a1", map[string]string{"env": "test"})
	if err := st.InsertRelease(ctx, store.Release{
		Version: "2.0.0", Signature: []byte{}, UploadedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	_, err := svc.Upgrade(ctx, &pb.UpgradeRequest{
		Selector: map[string]string{"env": "prod"}, Version: "2.0.0",
	})
	if status.Code(err) != codes.FailedPrecondition {
		t.Errorf("零匹配应 FailedPrecondition，实际 %v", err)
	}
}
