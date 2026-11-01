package enrollservice

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"log/slog"
	"net"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/crazychengmm/cagent/internal/server/ca"
	"github.com/crazychengmm/cagent/internal/server/enrollment"
	"github.com/crazychengmm/cagent/internal/server/store"
	pb "github.com/crazychengmm/cagent/proto/cagent/v1"
)

const certValidity = 90 * 24 * time.Hour

type testEnv struct {
	client pb.EnrollmentServiceClient
	store  *store.Store
	ca     *ca.CA
	tokens *enrollment.TokenService
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	ctx := context.Background()

	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	rootCA, err := ca.LoadOrCreate(filepath.Join(t.TempDir(), "ca"))
	if err != nil {
		t.Fatalf("ca: %v", err)
	}

	tokens := enrollment.NewTokenService(db, 168*time.Hour, time.Now)
	svc := New(db, tokens, rootCA, certValidity, time.Now, slog.New(slog.NewTextHandler(io.Discard, nil)))

	listener := bufconn.Listen(1024 * 1024)
	server := grpc.NewServer()
	pb.RegisterEnrollmentServiceServer(server, svc)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)

	conn, err := grpc.NewClient("passthrough://bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return listener.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { conn.Close() })

	return &testEnv{
		client: pb.NewEnrollmentServiceClient(conn),
		store:  db,
		ca:     rootCA,
		tokens: tokens,
	}
}

func makeCSR(t *testing.T) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := x509.CertificateRequest{Subject: pkix.Name{CommonName: "web-9"}}
	der, err := x509.CreateCertificateRequest(rand.Reader, &tmpl, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der})
}

func (e *testEnv) issueToken(t *testing.T) string {
	t.Helper()
	plaintext, _, err := e.tokens.CreateToken(context.Background(), "测试批", "alice", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return plaintext
}

func TestEnroll_Success(t *testing.T) {
	t.Parallel()

	env := newTestEnv(t)
	token := env.issueToken(t)
	ctx := context.Background()

	resp, err := env.client.Enroll(ctx, &pb.EnrollRequest{
		EnrollmentToken: token,
		CsrPem:          makeCSR(t),
		Hostname:        "web-9",
		Labels:          map[string]string{"env": "prod"},
	})
	if err != nil {
		t.Fatalf("Enroll: %v", err)
	}
	if resp.AgentId == "" || len(resp.CertificatePem) == 0 || len(resp.CaCertificatePem) == 0 {
		t.Fatalf("响应字段缺失: %+v", resp)
	}
	if resp.NotAfter.AsTime().Before(time.Now().Add(certValidity - time.Minute)) {
		t.Errorf("NotAfter 异常: %s", resp.NotAfter.AsTime())
	}

	// 签发证书可被 CA 验证，且 CN == 新分配的 agent UUID。
	if err := env.ca.VerifyAgentCert(resp.CertificatePem); err != nil {
		t.Fatalf("证书链校验失败: %v", err)
	}
	block, _ := pem.Decode(resp.CertificatePem)
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if cert.Subject.CommonName != resp.AgentId {
		t.Errorf("证书 CN = %q, 期望 %q", cert.Subject.CommonName, resp.AgentId)
	}

	// agents 表登记：序列号、标签、UUID 一致。
	agent, err := env.store.GetAgent(ctx, resp.AgentId)
	if err != nil {
		t.Fatalf("GetAgent: %v", err)
	}
	if agent.CertificateSerial == "" || agent.Labels["env"] != "prod" ||
		agent.Hostname != "web-9" || agent.CertNotAfter == nil {
		t.Errorf("agent 登记不符: %+v", agent)
	}

	// token 记录了使用时间且仍可复用（再注册一台成功）。
	_, err = env.client.Enroll(ctx, &pb.EnrollRequest{
		EnrollmentToken: token, CsrPem: makeCSR(t), Hostname: "web-10"})
	if err != nil {
		t.Errorf("可复用 token 第二次注册失败: %v", err)
	}

	logs, err := env.store.ListAudits(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	var enrolled int
	for _, l := range logs {
		if l.Action == store.ActionAgentEnroll {
			enrolled++
			if l.Actor != "alice" {
				t.Errorf("审计 actor = %q, 期望 alice", l.Actor)
			}
		}
	}
	if enrolled != 2 {
		t.Errorf("成功注册审计 = %d, 期望 2", enrolled)
	}
}

func TestEnroll_InvalidToken(t *testing.T) {
	t.Parallel()

	env := newTestEnv(t)
	_, err := env.client.Enroll(context.Background(), &pb.EnrollRequest{
		EnrollmentToken: "cagt_wrong", CsrPem: makeCSR(t), Hostname: "h",
	})
	if status.Code(err) != codes.PermissionDenied {
		t.Errorf("坏 token 应 PermissionDenied，实际 %v", err)
	}

	logs, _ := env.store.ListAudits(context.Background(), 10)
	var rejected int
	for _, l := range logs {
		if l.Action == store.ActionAgentRejected {
			rejected++
		}
	}
	if rejected != 1 {
		t.Errorf("应记 1 条 rejected 审计，实际 %d", rejected)
	}
}

func TestEnroll_BadCSR(t *testing.T) {
	t.Parallel()

	env := newTestEnv(t)
	token := env.issueToken(t)
	_, err := env.client.Enroll(context.Background(), &pb.EnrollRequest{
		EnrollmentToken: token, CsrPem: []byte("not a csr"), Hostname: "h",
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Errorf("坏 CSR 应 InvalidArgument，实际 %v", err)
	}
}

func TestEnroll_RequestValidation(t *testing.T) {
	t.Parallel()

	env := newTestEnv(t)
	token := env.issueToken(t)
	ctx := context.Background()

	cases := []struct {
		name string
		req  *pb.EnrollRequest
	}{
		{"空 token", &pb.EnrollRequest{CsrPem: makeCSR(t), Hostname: "h"}},
		{"空 hostname", &pb.EnrollRequest{EnrollmentToken: token, CsrPem: makeCSR(t)}},
		{"空 csr", &pb.EnrollRequest{EnrollmentToken: token, Hostname: "h"}},
		{"标签过多", func() *pb.EnrollRequest {
			labels := map[string]string{}
			for i := 0; i < maxLabelCount+1; i++ {
				labels["k"+string(rune('a'+i%26))+string(rune('a'+i/26))] = "v"
			}
			return &pb.EnrollRequest{EnrollmentToken: token, CsrPem: makeCSR(t), Hostname: "h", Labels: labels}
		}()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := env.client.Enroll(ctx, tc.req); status.Code(err) != codes.InvalidArgument {
				t.Errorf("应 InvalidArgument，实际 %v", err)
			}
		})
	}
}
