package controlservice

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	"github.com/crazychengmm/cagent/internal/agent/control"
	"github.com/crazychengmm/cagent/internal/agent/identity"
	"github.com/crazychengmm/cagent/internal/server/ca"
	"github.com/crazychengmm/cagent/internal/server/store"
	pb "github.com/crazychengmm/cagent/proto/cagent/v1"
)

type controlHarness struct {
	addr   string
	store  *store.Store
	rootCA *ca.CA
	svc    *Service
	pkgDir string
	stop   func()
}

func startControlHarness(t *testing.T) *controlHarness {
	t.Helper()
	caDir := filepath.Join(t.TempDir(), "ca")
	rootCA, err := ca.LoadOrCreate(caDir)
	if err != nil {
		t.Fatal(err)
	}
	serverCert, err := rootCA.EnsureServerCertificate(caDir,
		[]string{"localhost", "127.0.0.1"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	pkgDir := filepath.Join(t.TempDir(), "releases")
	os.MkdirAll(pkgDir, 0o700)
	svc := New(db, rootCA, time.Hour, pkgDir, time.Now, logger)

	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(rootCA.CACertPEM())
	server := grpc.NewServer(grpc.Creds(credentials.NewTLS(&tls.Config{
		Certificates: []tls.Certificate{*serverCert},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    pool,
		MinVersion:   tls.VersionTLS12,
	})))
	pb.RegisterControlServiceServer(server, svc)
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go server.Serve(lis)
	return &controlHarness{
		addr:   lis.Addr().String(),
		store:  db,
		rootCA: rootCA,
		svc:    svc,
		pkgDir: pkgDir,
		stop:   server.Stop,
	}
}

// provisionAgent 在库中登记一台 agent 并构造其本地身份材料。
func (h *controlHarness) provisionAgent(t *testing.T, agentID string) *identity.Identity {
	t.Helper()
	key, err := identity.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	csrPEM, err := identity.MakeCSR(key, "host-"+agentID)
	if err != nil {
		t.Fatal(err)
	}
	serial, err := ca.NewSerial()
	if err != nil {
		t.Fatal(err)
	}
	notAfter := time.Now().Add(time.Hour)
	certPEM, err := h.rootCA.SignAgentCSR(csrPEM, agentID, serial, notAfter)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := h.store.InsertAgent(context.Background(), store.Agent{
		AgentID:           agentID,
		Hostname:          "host-" + agentID,
		Labels:            map[string]string{},
		CertificateSerial: serial.Text(16),
		CertNotAfter:      &notAfter,
		CreatedAt:         now,
		UpdatedAt:         now,
	}); err != nil {
		t.Fatal(err)
	}
	dataDir := filepath.Join(t.TempDir(), "agent")
	if err := identity.Save(dataDir, agentID, key, certPEM, h.rootCA.CACertPEM()); err != nil {
		t.Fatal(err)
	}
	ident, err := identity.Load(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	return ident
}

func (h *controlHarness) runAgentLoop(t *testing.T, ident *identity.Identity,
	version string, labels map[string]string) (context.CancelFunc, string) {
	t.Helper()
	dataDir := filepath.Join(t.TempDir(), "loop-agent")
	// 复制身份到 loop 数据目录（续签替换写在这个目录）。
	if err := identity.Save(dataDir, ident.AgentID, ident.Key, ident.CertPEM, ident.CAPEM); err != nil {
		t.Fatal(err)
	}
	loop, err := control.New(control.Config{
		Identity:          ident,
		ServerAddr:        h.addr,
		DataDir:           dataDir,
		Version:           version,
		Labels:            labels,
		HeartbeatInterval: 50 * time.Millisecond,
		Logger:            slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = loop.Run(ctx) }()
	t.Cleanup(cancel)
	return cancel, dataDir
}

func waitStore(t *testing.T, db *store.Store, agentID string, cond func(*store.Agent) bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		agent, err := db.GetAgent(context.Background(), agentID)
		if err == nil && cond(agent) {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("存储条件等待超时")
}

func TestControlFlow_HeartbeatMakesOnline(t *testing.T) {
	h := startControlHarness(t)
	defer h.stop()

	ident := h.provisionAgent(t, "ag-hb")
	cancel, _ := h.runAgentLoop(t, ident, "42.0.0", map[string]string{"env": "test"})
	defer cancel()

	waitStore(t, h.store, "ag-hb", func(a *store.Agent) bool {
		return a.Status == store.StatusOnline && a.Version == "42.0.0" && a.Labels["env"] == "test"
	})

	// 在线注册表可见，且 SendToAgent 不返回离线错误。
	var online bool
	for _, id := range h.svc.OnlineAgentIDs() {
		if id == "ag-hb" {
			online = true
		}
	}
	if !online {
		t.Error("OnlineAgentIDs 应包含该 agent")
	}
}

func TestControlFlow_SweptOfflineAfterTimeout(t *testing.T) {
	h := startControlHarness(t)
	defer h.stop()

	ident := h.provisionAgent(t, "ag-off")
	cancel, _ := h.runAgentLoop(t, ident, "1.0.0", nil)
	waitStore(t, h.store, "ag-off", func(a *store.Agent) bool {
		return a.Status == store.StatusOnline
	})

	cancel() // agent 停止心跳
	time.Sleep(250 * time.Millisecond)
	sweepOnce(context.Background(), h.store, 200*time.Millisecond,
		slog.New(slog.NewTextHandler(io.Discard, nil)))

	agent, err := h.store.GetAgent(context.Background(), "ag-off")
	if err != nil {
		t.Fatal(err)
	}
	if agent.Status != store.StatusOffline {
		t.Errorf("超时扫描后应为 offline，实际 %s", agent.Status)
	}
}

func TestControlFlow_RevocationKicksLongConnection(t *testing.T) {
	h := startControlHarness(t)
	defer h.stop()

	ident := h.provisionAgent(t, "ag-rev")
	cancel, _ := h.runAgentLoop(t, ident, "1.0.0", nil)
	defer cancel()
	waitStore(t, h.store, "ag-rev", func(a *store.Agent) bool {
		return a.Status == store.StatusOnline
	})

	if _, err := h.store.RevokeAgent(context.Background(), "ag-rev", time.Now()); err != nil {
		t.Fatal(err)
	}
	// 下一次心跳落库失败 → 服务端结束流；agent 重连时 mTLS 吊销检查拒绝。
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		found := false
		for _, id := range h.svc.OnlineAgentIDs() {
			if id == "ag-rev" {
				found = true
			}
		}
		if !found {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("吊销后长连接应被踢下线且无法重连")
}

func TestControlFlow_SendToOfflineAgent(t *testing.T) {
	h := startControlHarness(t)
	defer h.stop()

	err := h.svc.SendToAgent("ag-ghost", &pb.ServerMessage{})
	if err != ErrAgentOffline {
		t.Errorf("离线 agent 应返回 ErrAgentOffline，实际 %v", err)
	}
}
