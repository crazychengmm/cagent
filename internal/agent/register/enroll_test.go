package register

import (
	"context"
	"crypto/tls"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	"github.com/crazychengmm/cagent/internal/agent/identity"
	"github.com/crazychengmm/cagent/internal/agent/trust"
	"github.com/crazychengmm/cagent/internal/server/ca"
	"github.com/crazychengmm/cagent/internal/server/enrollment"
	"github.com/crazychengmm/cagent/internal/server/enrollservice"
	"github.com/crazychengmm/cagent/internal/server/store"
	pb "github.com/crazychengmm/cagent/proto/cagent/v1"
)

type testServer struct {
	addr   string
	caPEM  []byte
	tokens *enrollment.TokenService
}

func startEnrollServer(t *testing.T) *testServer {
	t.Helper()
	caDir := filepath.Join(t.TempDir(), "ca")
	rootCA, err := ca.LoadOrCreate(caDir)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := rootCA.EnsureServerCertificate(caDir,
		[]string{"localhost", "127.0.0.1"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	tokens := enrollment.NewTokenService(db, time.Hour, time.Now)
	svc := enrollservice.New(db, tokens, rootCA, time.Hour, time.Now,
		slog.New(slog.NewTextHandler(io.Discard, nil)))

	server := grpc.NewServer(grpc.Creds(credentials.NewTLS(
		&tls.Config{Certificates: []tls.Certificate{*cert}})))
	pb.RegisterEnrollmentServiceServer(server, svc)
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go server.Serve(lis)
	t.Cleanup(server.Stop)

	return &testServer{addr: lis.Addr().String(), caPEM: rootCA.CACertPEM(), tokens: tokens}
}

func writeCA(t *testing.T, pem []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ca.crt")
	if err := os.WriteFile(path, pem, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestEnroll_SuccessAndPersist(t *testing.T) {
	t.Parallel()

	srv := startEnrollServer(t)
	plaintext, _, err := srv.tokens.CreateToken(context.Background(), "", "alice", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	caFile := writeCA(t, srv.caPEM)
	tlsCfg, err := trust.EnrollmentTLS(caFile, "")
	if err != nil {
		t.Fatal(err)
	}
	key, err := identity.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := Enroll(ctx, srv.addr, tlsCfg, plaintext, "web-9",
		map[string]string{"env": "prod"}, key)
	if err != nil {
		t.Fatalf("Enroll: %v", err)
	}
	if result.AgentID == "" || len(result.CertPEM) == 0 || len(result.CAPEM) == 0 {
		t.Fatalf("注册结果不完整: %+v", result)
	}
	if time.Until(result.NotAfter) > time.Hour || time.Until(result.NotAfter) < time.Hour-time.Minute {
		t.Errorf("NotAfter 异常: %s", result.NotAfter)
	}

	// 注册结果可直接作为本地身份持久化并重新加载。
	dataDir := filepath.Join(t.TempDir(), "agent")
	if err := identity.Save(dataDir, result.AgentID, key, result.CertPEM, result.CAPEM); err != nil {
		t.Fatal(err)
	}
	ident, err := identity.Load(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if ident.AgentID != result.AgentID {
		t.Error("持久化身份 UUID 不一致")
	}
}

func TestEnroll_BadToken(t *testing.T) {
	t.Parallel()

	srv := startEnrollServer(t)
	tlsCfg, err := trust.EnrollmentTLS(writeCA(t, srv.caPEM), "")
	if err != nil {
		t.Fatal(err)
	}
	key, _ := identity.GenerateKey()
	_, err = Enroll(context.Background(), srv.addr, tlsCfg, "cagt_wrong", "h", nil, key)
	if err == nil || !strings.Contains(err.Error(), "注册被拒绝") {
		t.Errorf("坏 token 应被映射为业务错误，实际 %v", err)
	}
}

func TestEnroll_AnchorMismatchFailsBeforeToken(t *testing.T) {
	t.Parallel()

	srv := startEnrollServer(t)
	token, _, _ := srv.tokens.CreateToken(context.Background(), "", "alice", time.Hour)

	// 用另一套 CA 做锚点：握手即失败，token 不可能到达 server。
	otherCA, err := ca.LoadOrCreate(filepath.Join(t.TempDir(), "other-ca"))
	if err != nil {
		t.Fatal(err)
	}
	tlsCfg, err := trust.EnrollmentTLS(writeCA(t, otherCA.CACertPEM()), "")
	if err != nil {
		t.Fatal(err)
	}
	key, _ := identity.GenerateKey()
	_, err = Enroll(context.Background(), srv.addr, tlsCfg, token, "h", nil, key)
	if err == nil {
		t.Error("锚点不匹配必须在 TLS 层失败")
	}
}

func TestEnroll_RequiresAnchorAndToken(t *testing.T) {
	t.Parallel()

	key, _ := identity.GenerateKey()
	if _, err := Enroll(context.Background(), "127.0.0.1:1", nil, "t", "h", nil, key); err == nil {
		t.Error("nil TLS 配置应报错")
	}

	srv := startEnrollServer(t)
	cfg, _ := trust.EnrollmentTLS(writeCA(t, srv.caPEM), "")
	if _, err := Enroll(context.Background(), srv.addr, cfg, "", "h", nil, key); err == nil {
		t.Error("空 token 应报错")
	}
}
