package control

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"io"
	"log/slog"
	"net"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/crazychengmm/cagent/internal/agent/identity"
	"github.com/crazychengmm/cagent/internal/server/ca"
	pb "github.com/crazychengmm/cagent/proto/cagent/v1"
)

// fakeControl 是可编排的 ControlService 测试服务。
type fakeControl struct {
	pb.UnimplementedControlServiceServer

	streams     atomic.Int32
	heartbeats  chan *pb.Heartbeat
	renewed     atomic.Int32
	closeFirst  bool
	newCertPEM  func() []byte
	sendOnEnter []*pb.ServerMessage
	sent        chan struct{}
}

func (f *fakeControl) Control(stream pb.ControlService_ControlServer) error {
	n := f.streams.Add(1)
	if f.closeFirst && n == 1 {
		return nil // 第一条流立即结束，模拟断线
	}
	if n == 1 && len(f.sendOnEnter) > 0 {
		for _, msg := range f.sendOnEnter {
			if err := stream.Send(msg); err != nil {
				return err
			}
		}
		f.sent <- struct{}{}
	}
	for {
		msg, err := stream.Recv()
		if err != nil {
			return err
		}
		if hb := msg.GetHeartbeat(); hb != nil {
			select {
			case f.heartbeats <- hb:
			default:
			}
		}
	}
}

func (f *fakeControl) RenewCert(_ context.Context, _ *pb.RenewCertRequest) (*pb.RenewCertResponse, error) {
	f.renewed.Add(1)
	certPEM := f.newCertPEM()
	block, _ := pem.Decode(certPEM)
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, err
	}
	return &pb.RenewCertResponse{CertificatePem: certPEM, NotAfter: timestamppb.New(cert.NotAfter)}, nil
}

type testEnv struct {
	addr     string
	rootCA   *ca.CA
	dataDir  string
	ident    *identity.Identity
	server   *grpc.Server
	fake     *fakeControl
	shutdown func()
}

func startTestEnv(t *testing.T, fake *fakeControl, certNotAfter time.Time) *testEnv {
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

	// 构造 agent 身份（CA 签发的客户端证书）。
	key, err := identity.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	csrPEM, err := identity.MakeCSR(key, "host1")
	if err != nil {
		t.Fatal(err)
	}
	serial, err := ca.NewSerial()
	if err != nil {
		t.Fatal(err)
	}
	certPEM, err := rootCA.SignAgentCSR(csrPEM, "ag-test", serial, certNotAfter)
	if err != nil {
		t.Fatal(err)
	}
	dataDir := filepath.Join(t.TempDir(), "agent")
	if err := identity.Save(dataDir, "ag-test", key, certPEM, rootCA.CACertPEM()); err != nil {
		t.Fatal(err)
	}
	ident, err := identity.Load(dataDir)
	if err != nil {
		t.Fatal(err)
	}

	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(rootCA.CACertPEM())
	server := grpc.NewServer(grpc.Creds(credentials.NewTLS(&tls.Config{
		Certificates: []tls.Certificate{*serverCert},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    pool,
		MinVersion:   tls.VersionTLS12,
	})))
	pb.RegisterControlServiceServer(server, fake)
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go server.Serve(lis)

	return &testEnv{
		addr:     lis.Addr().String(),
		rootCA:   rootCA,
		dataDir:  dataDir,
		ident:    ident,
		server:   server,
		fake:     fake,
		shutdown: server.Stop,
	}
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func runLoop(t *testing.T, env *testEnv, cfg Config) (context.CancelFunc, *sync.Mutex, *[]bool) {
	t.Helper()
	var mu sync.Mutex
	states := make([]bool, 0, 8)
	cfg.Identity = env.ident
	cfg.ServerAddr = env.addr
	cfg.DataDir = env.dataDir
	if cfg.Logger == nil {
		cfg.Logger = discardLogger()
	}
	cfg.OnStateChange = func(connected bool) {
		mu.Lock()
		states = append(states, connected)
		mu.Unlock()
	}
	loop, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = loop.Run(ctx); close(done) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
		}
	})
	return cancel, &mu, &states
}

func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("条件等待超时")
}

func TestLoop_Heartbeats(t *testing.T) {
	fake := &fakeControl{heartbeats: make(chan *pb.Heartbeat, 16)}
	env := startTestEnv(t, fake, time.Now().Add(time.Hour))
	t.Cleanup(env.shutdown)

	cancel, _, _ := runLoop(t, env, Config{HeartbeatInterval: 50 * time.Millisecond,
		Version: "1.2.3", Labels: map[string]string{"env": "prod"}})
	defer cancel()

	first := <-fake.heartbeats
	if first.AgentId != "ag-test" || first.Version != "1.2.3" || first.Labels["env"] != "prod" {
		t.Errorf("心跳字段不符: %+v", first)
	}
	got := 1
	deadline := time.After(time.Second)
	for got < 3 {
		select {
		case <-fake.heartbeats:
			got++
		case <-deadline:
			t.Fatalf("只收到 %d 个心跳", got)
		}
	}
}

func TestLoop_ReconnectsAfterStreamDrop(t *testing.T) {
	fake := &fakeControl{heartbeats: make(chan *pb.Heartbeat, 16), closeFirst: true}
	env := startTestEnv(t, fake, time.Now().Add(time.Hour))
	t.Cleanup(env.shutdown)

	cancel, mu, states := runLoop(t, env, Config{
		HeartbeatInterval: 50 * time.Millisecond})
	defer cancel()

	waitFor(t, 3*time.Second, func() bool { return fake.streams.Load() >= 2 })
	// 第二条流上也能收到心跳。
	waitFor(t, time.Second, func() bool {
		select {
		case <-fake.heartbeats:
			return true
		default:
			return false
		}
	})
	mu.Lock()
	defer mu.Unlock()
	var sawDown, sawUpAgain bool
	for _, connected := range *states {
		if !connected {
			sawDown = true
		}
		if sawDown && connected {
			sawUpAgain = true
		}
	}
	if !sawDown || !sawUpAgain {
		t.Errorf("状态序列未体现断线重连: %v", *states)
	}
}

func TestLoop_AutoRenewAndReconnect(t *testing.T) {
	fake := &fakeControl{heartbeats: make(chan *pb.Heartbeat, 16)}
	env := startTestEnv(t, fake, time.Now().Add(time.Hour))
	t.Cleanup(env.shutdown)

	// server 续签时签发一张 2h 后的新证书。
	fake.newCertPEM = func() []byte {
		key := env.ident.Key
		csrPEM, _ := identity.MakeCSR(key, "ag-test")
		serial, _ := ca.NewSerial()
		pemBytes, err := env.rootCA.SignAgentCSR(csrPEM, "ag-test", serial, time.Now().Add(2*time.Hour))
		if err != nil {
			t.Error(err)
		}
		return pemBytes
	}

	oldCertPEM := append([]byte(nil), env.ident.CertPEM...)
	cancel, _, _ := runLoop(t, env, Config{
		HeartbeatInterval: 50 * time.Millisecond,
		RenewBefore:       60 * time.Minute, // 1h 证书剩余续签窗口为 0
	})
	defer cancel()

	waitFor(t, 5*time.Second, func() bool { return fake.renewed.Load() >= 1 })
	waitFor(t, 5*time.Second, func() bool { return fake.streams.Load() >= 2 })

	reloaded, err := identity.Load(env.dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if string(reloaded.CertPEM) == string(oldCertPEM) {
		t.Error("续签后本地证书应已替换")
	}
}

type recordingHandler struct {
	dispatches chan *pb.JobDispatch
	cancels    chan string
	upgrades   chan *pb.UpgradeCommand
}

func (h *recordingHandler) OnDispatch(_ context.Context, _ Sender, d *pb.JobDispatch) {
	h.dispatches <- d
}
func (h *recordingHandler) OnCancel(jobID string) { h.cancels <- jobID }
func (h *recordingHandler) OnUpgrade(_ context.Context, _ Sender, u *pb.UpgradeCommand) {
	h.upgrades <- u
}

func TestLoop_DispatchesMessages(t *testing.T) {
	fake := &fakeControl{
		heartbeats: make(chan *pb.Heartbeat, 16),
		sent:       make(chan struct{}, 1),
		sendOnEnter: []*pb.ServerMessage{
			{Payload: &pb.ServerMessage_JobDispatch{JobDispatch: &pb.JobDispatch{JobId: "j-1", Command: "uptime"}}},
			{Payload: &pb.ServerMessage_JobCancel{JobCancel: &pb.JobCancel{JobId: "j-1"}}},
			{Payload: &pb.ServerMessage_UpgradeCommand{UpgradeCommand: &pb.UpgradeCommand{JobId: "u-1", Version: "9.9.9"}}},
		},
	}
	env := startTestEnv(t, fake, time.Now().Add(time.Hour))
	t.Cleanup(env.shutdown)

	handler := &recordingHandler{
		dispatches: make(chan *pb.JobDispatch, 1),
		cancels:    make(chan string, 1),
		upgrades:   make(chan *pb.UpgradeCommand, 1),
	}
	cancel, _, _ := runLoop(t, env, Config{
		HeartbeatInterval: time.Hour,
		JobHandler:        handler,
		UpgradeHandler:    handler,
	})
	defer cancel()

	select {
	case d := <-handler.dispatches:
		if d.JobId != "j-1" || d.Command != "uptime" {
			t.Errorf("dispatch 不符: %+v", d)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("未收到 dispatch")
	}
	select {
	case id := <-handler.cancels:
		if id != "j-1" {
			t.Errorf("cancel id = %q", id)
		}
	case <-time.After(time.Second):
		t.Fatal("未收到 cancel")
	}
	select {
	case u := <-handler.upgrades:
		if u.Version != "9.9.9" {
			t.Errorf("upgrade = %+v", u)
		}
	case <-time.After(time.Second):
		t.Fatal("未收到 upgrade")
	}
}

func TestLoop_UnreachableServerKeepsRetrying(t *testing.T) {
	// 不启动真实服务，指向一个空闲端口；Run 必须可被 ctx 取消且返回 nil。
	caDir := filepath.Join(t.TempDir(), "ca")
	rootCA, _ := ca.LoadOrCreate(caDir)
	key, _ := identity.GenerateKey()
	csrPEM, _ := identity.MakeCSR(key, "h")
	serial, _ := ca.NewSerial()
	certPEM, _ := rootCA.SignAgentCSR(csrPEM, "ag-x", serial, time.Now().Add(time.Hour))
	dataDir := filepath.Join(t.TempDir(), "agent")
	_ = identity.Save(dataDir, "ag-x", key, certPEM, rootCA.CACertPEM())
	ident, _ := identity.Load(dataDir)

	loop, err := New(Config{
		Identity:          ident,
		ServerAddr:        "127.0.0.1:1",
		DataDir:           dataDir,
		HeartbeatInterval: 50 * time.Millisecond,
		Logger:            discardLogger(),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- loop.Run(ctx) }()
	time.Sleep(150 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("ctx 取消应返回 nil，实际 %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run 未随 ctx 取消退出")
	}
}
