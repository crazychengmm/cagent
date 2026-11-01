package controlservice

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"

	"github.com/crazychengmm/cagent/internal/server/ca"
	"github.com/crazychengmm/cagent/internal/server/store"
	pb "github.com/crazychengmm/cagent/proto/cagent/v1"
)

const testCertValidity = time.Hour

type fixture struct {
	svc   *Service
	store *store.Store
	ca    *ca.CA
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	rootCA, err := ca.LoadOrCreate(filepath.Join(t.TempDir(), "ca"))
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{
		svc:   New(db, rootCA, testCertValidity, time.Now, slog.New(slog.NewTextHandler(io.Discard, nil))),
		store: db,
		ca:    rootCA,
	}
}

func makeCSR(t *testing.T, cn string) ([]byte, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := x509.CertificateRequest{Subject: pkix.Name{CommonName: cn}}
	der, err := x509.CreateCertificateRequest(rand.Reader, &tmpl, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der}), key
}

// registerAgent 模拟注册结果：签发证书并落 agents 表，返回证书对象。
func (f *fixture) registerAgent(t *testing.T, agentID string) *x509.Certificate {
	t.Helper()
	csrPEM, _ := makeCSR(t, agentID)
	serial, err := ca.NewSerial()
	if err != nil {
		t.Fatal(err)
	}
	notAfter := time.Now().Add(-time.Minute) // 已过期的旧证，续签才有意义
	certPEM, err := f.ca.SignAgentCSR(csrPEM, agentID, serial, notAfter)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(certPEM)
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := f.store.InsertAgent(context.Background(), store.Agent{
		AgentID:           agentID,
		Hostname:          "h",
		Labels:            map[string]string{},
		CertificateSerial: serial.Text(16),
		CertNotAfter:      &notAfter,
		CreatedAt:         now,
		UpdatedAt:         now,
	}); err != nil {
		t.Fatal(err)
	}
	return cert
}

func mTLSContext(cert *x509.Certificate) context.Context {
	ctx := context.Background()
	if cert == nil {
		return ctx
	}
	return peer.NewContext(ctx, &peer.Peer{
		AuthInfo: credentials.TLSInfo{
			State: tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert}},
		},
	})
}

func TestRenewCert_Success(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	oldCert := f.registerAgent(t, "ag-1")
	oldAgent, _ := f.store.GetAgent(context.Background(), "ag-1")

	csrPEM, _ := makeCSR(t, "ag-1")
	resp, err := f.svc.RenewCert(mTLSContext(oldCert), &pb.RenewCertRequest{CsrPem: csrPEM})
	if err != nil {
		t.Fatalf("RenewCert: %v", status.Convert(err))
	}
	if err := f.ca.VerifyAgentCert(resp.CertificatePem); err != nil {
		t.Fatalf("新证书链校验失败: %v", err)
	}
	block, _ := pem.Decode(resp.CertificatePem)
	newCert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if newCert.Subject.CommonName != "ag-1" {
		t.Errorf("CN = %q", newCert.Subject.CommonName)
	}

	updated, _ := f.store.GetAgent(context.Background(), "ag-1")
	if updated.CertificateSerial == oldAgent.CertificateSerial {
		t.Error("续签后序列号应变化（旧证即时失效）")
	}
	if !updated.CertNotAfter.After(time.Now().Add(testCertValidity - time.Minute)) {
		t.Errorf("新到期时间异常: %s", updated.CertNotAfter)
	}
}

func TestRenewCert_Failures(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	cert := f.registerAgent(t, "ag-2")
	csrPEM, _ := makeCSR(t, "ag-2")

	tests := []struct {
		name string
		ctx  context.Context
		req  *pb.RenewCertRequest
		want codes.Code
	}{
		{"无 peer 上下文", context.Background(), &pb.RenewCertRequest{CsrPem: csrPEM}, codes.Unauthenticated},
		{"非 TLS 认证信息", peer.NewContext(context.Background(), &peer.Peer{}), &pb.RenewCertRequest{CsrPem: csrPEM}, codes.Unauthenticated},
		{"未知 agent", mTLSContext(func() *x509.Certificate {
			_, c := f.issueUnknownCert(t)
			return c
		}()), &pb.RenewCertRequest{CsrPem: csrPEM}, codes.Unauthenticated},
		{"空 CSR", mTLSContext(cert), &pb.RenewCertRequest{}, codes.InvalidArgument},
		{"坏 CSR", mTLSContext(cert), &pb.RenewCertRequest{CsrPem: []byte("x")}, codes.InvalidArgument},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := f.svc.RenewCert(tt.ctx, tt.req)
			if status.Code(err) != tt.want {
				t.Errorf("code = %v, 期望 %v, err=%v", status.Code(err), tt.want, err)
			}
		})
	}
}

func TestRenewCert_RejectsRevokedAgent(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	cert := f.registerAgent(t, "ag-3")
	csrPEM, _ := makeCSR(t, "ag-3")

	if _, err := f.store.RevokeAgent(context.Background(), "ag-3", time.Now()); err != nil {
		t.Fatal(err)
	}
	_, err := f.svc.RenewCert(mTLSContext(cert), &pb.RenewCertRequest{CsrPem: csrPEM})
	if status.Code(err) != codes.PermissionDenied {
		t.Errorf("吊销机续签应 PermissionDenied，实际 %v", err)
	}
}

// issueUnknownCert 生成一张 CN 不在库中的 CA 签发证书（测试用）。
func (f *fixture) issueUnknownCert(t *testing.T) ([]byte, *x509.Certificate) {
	t.Helper()
	csrPEM, _ := makeCSR(t, "ag-ghost")
	serial, _ := ca.NewSerial()
	certPEM, err := f.ca.SignAgentCSR(csrPEM, "ag-ghost", serial, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(certPEM)
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	return certPEM, cert
}
