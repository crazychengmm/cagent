package serve

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
	"math/big"
	"net"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/crazychengmm/cagent/internal/server/ca"
	"github.com/crazychengmm/cagent/internal/server/controlservice"
	"github.com/crazychengmm/cagent/internal/server/enrollment"
	"github.com/crazychengmm/cagent/internal/server/enrollservice"
	"github.com/crazychengmm/cagent/internal/server/store"
	pb "github.com/crazychengmm/cagent/proto/cagent/v1"
)

func freePort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	return addr
}

type harness struct {
	enrollAddr  string
	controlAddr string
	adminAddr   string
	cancel      context.CancelFunc
	store       *store.Store
	caPEM       []byte
	tokens      *enrollment.TokenService
}

func startHarness(t *testing.T) *harness {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())

	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

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
	discardLogger := slog.New(slog.NewTextHandler(io.Discard, nil))
	tokens := enrollment.NewTokenService(db, time.Hour, time.Now)
	enrollSvc := enrollservice.New(db, tokens, rootCA, time.Hour, time.Now, discardLogger)
	controlSvc := controlservice.New(db, rootCA, time.Hour, time.Now, discardLogger)

	h := &harness{
		enrollAddr:  freePort(t),
		controlAddr: freePort(t),
		adminAddr:   freePort(t),
		cancel:      cancel,
		store:       db,
		caPEM:       rootCA.CACertPEM(),
		tokens:      tokens,
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- Run(ctx, Deps{
			EnrollmentListen: h.enrollAddr,
			ControlListen:    h.controlAddr,
			AdminListen:      h.adminAddr,
			EnrollService:    enrollSvc,
			RegisterControl:  func(s *grpc.Server) { pb.RegisterControlServiceServer(s, controlSvc) },
			RegisterAdmin:    func(s *grpc.Server) { pb.RegisterEnrollmentServiceServer(s, enrollSvc) },
			ServerCert:       *serverCert,
			CAPEM:            h.caPEM,
			AdminToken:       "cactl_admin",
			Store:            db,
			Logger:           slog.New(slog.NewTextHandler(io.Discard, nil)),
		})
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-errCh:
		case <-time.After(12 * time.Second):
			t.Fatal("优雅停止超时")
		}
	})

	waitForPort(t, h.enrollAddr)
	waitForPort(t, h.controlAddr)
	waitForPort(t, h.adminAddr)
	return h
}

func waitForPort(t *testing.T, addr string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if conn, err := net.DialTimeout("tcp", addr, 100*time.Millisecond); err == nil {
			conn.Close()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("端口 %s 未就绪", addr)
}

func (h *harness) caPool() *x509.CertPool {
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(h.caPEM)
	return pool
}

func (h *harness) createToken(t *testing.T) string {
	t.Helper()
	plaintext, _, err := h.tokens.CreateToken(context.Background(), "", "alice", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return plaintext
}

// enrollAgent 通过真实注册流程取得一张 agent 客户端证书与对应私钥。
func (h *harness) enrollAgent(t *testing.T, token string) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := x509.CertificateRequest{Subject: pkix.Name{CommonName: "host1"}}
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, &tmpl, key)
	if err != nil {
		t.Fatal(err)
	}
	csrPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER})

	conn, err := grpc.NewClient(h.enrollAddr,
		grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{RootCAs: h.caPool()})))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	resp, err := pb.NewEnrollmentServiceClient(conn).Enroll(context.Background(),
		&pb.EnrollRequest{EnrollmentToken: token, CsrPem: csrPEM, Hostname: "host1"})
	if err != nil {
		t.Fatalf("enroll: %v", err)
	}
	block, _ := pem.Decode(resp.CertificatePem)
	return tls.Certificate{Certificate: [][]byte{block.Bytes}, PrivateKey: key}
}

// dialControlConn 建立到 control 端口的 mTLS 连接；cert 为 nil 时不携带客户端证书。
func dialControlConn(t *testing.T, addr string, caPEM []byte, cert *tls.Certificate) *grpc.ClientConn {
	t.Helper()
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(caPEM)
	cfg := &tls.Config{RootCAs: pool}
	if cert != nil {
		cfg.Certificates = []tls.Certificate{*cert}
	}
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(credentials.NewTLS(cfg)))
	if err != nil {
		t.Fatal(err)
	}
	return conn
}

// openControlStream 建立 Control 双向流并返回首个 Recv 错误；
// 未知服务的 Unimplemented 要在 Recv 才对客户端可见。
func openControlStream(t *testing.T, addr string, caPEM []byte, cert *tls.Certificate) error {
	t.Helper()
	conn := dialControlConn(t, addr, caPEM, cert)
	defer conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	stream, err := pb.NewControlServiceClient(conn).Control(ctx)
	if err != nil {
		return err
	}
	_ = stream.CloseSend()
	_, err = stream.Recv()
	return err
}

func csrWithKey(t *testing.T, key *ecdsa.PrivateKey, cn string) []byte {
	t.Helper()
	tmpl := x509.CertificateRequest{Subject: pkix.Name{CommonName: cn}}
	der, err := x509.CreateCertificateRequest(rand.Reader, &tmpl, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der})
}

func TestServe_RenewCertOverMutualTLS(t *testing.T) {
	h := startHarness(t)
	oldCert := h.enrollAgent(t, h.createToken(t))

	// 用旧证书通过 mTLS 调续签（沿用同一私钥）。
	conn := dialControlConn(t, h.controlAddr, h.caPEM, &oldCert)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	resp, err := pb.NewControlServiceClient(conn).RenewCert(ctx,
		&pb.RenewCertRequest{CsrPem: csrWithKey(t, oldCert.PrivateKey.(*ecdsa.PrivateKey), "host1")})
	if err != nil {
		t.Fatalf("RenewCert: %v", err)
	}
	block, _ := pem.Decode(resp.CertificatePem)
	if block == nil {
		t.Fatal("续签返回不是 PEM")
	}
	newCert := tls.Certificate{Certificate: [][]byte{block.Bytes}, PrivateKey: oldCert.PrivateKey}

	// 旧序列号已失效：旧证书的新连接必须被拒。
	if err := openControlStream(t, h.controlAddr, h.caPEM, &oldCert); status.Code(err) != codes.Unavailable {
		t.Errorf("续签后旧证书应被拒(Unavailable)，实际 code=%v err=%v", status.Code(err), err)
	}

	// 新证书可正常通过 mTLS（握手通过即不会是 Unavailable）。
	if err := openControlStream(t, h.controlAddr, h.caPEM, &newCert); status.Code(err) == codes.Unavailable {
		t.Errorf("新证书应通过 mTLS，实际 code=%v err=%v", status.Code(err), err)
	}
}

// strangerCert 生成一张由外来自签 CA 签发的客户端证书。
func strangerCert(t *testing.T) tls.Certificate {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	caTmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "stranger-ca"},
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	strangerCA, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}

	clientKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	leafTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "intruder"},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTmpl, strangerCA, &clientKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{leafDER, caDER}, PrivateKey: clientKey}
}

func TestServe_EnrollmentPortOneWayTLS(t *testing.T) {
	h := startHarness(t)
	cert := h.enrollAgent(t, h.createToken(t))
	if len(cert.Certificate) == 0 {
		t.Fatal("应通过单向 TLS 完成注册并取得证书")
	}
}

func TestServe_ControlPortMutualTLS(t *testing.T) {
	h := startHarness(t)

	t.Run("无客户端证书被拒", func(t *testing.T) {
		err := openControlStream(t, h.controlAddr, h.caPEM, nil)
		if status.Code(err) != codes.Unavailable {
			t.Errorf("无证书应握手失败(Unavailable)，实际 code=%v err=%v", status.Code(err), err)
		}
	})

	t.Run("有效 agent 证书通过", func(t *testing.T) {
		cert := h.enrollAgent(t, h.createToken(t))
		err := openControlStream(t, h.controlAddr, h.caPEM, &cert)
		// Control 已实现：握手通过后客户端立即半关闭，得到的是正常结束
		// （OK/Unknown），而非握手失败 Unavailable。
		if status.Code(err) == codes.Unavailable {
			t.Errorf("有效证书不应握手失败，实际 code=%v err=%v", status.Code(err), err)
		}
	})

	t.Run("非内置 CA 签发证书被拒", func(t *testing.T) {
		cert := strangerCert(t)
		err := openControlStream(t, h.controlAddr, h.caPEM, &cert)
		if status.Code(err) != codes.Unavailable {
			t.Errorf("外来证书应握手失败，实际 code=%v err=%v", status.Code(err), err)
		}
	})
}

func TestServe_RevokedCertificateRejected(t *testing.T) {
	h := startHarness(t)
	cert := h.enrollAgent(t, h.createToken(t))

	agents, err := h.store.ListAgents(context.Background())
	if err != nil || len(agents) != 1 {
		t.Fatalf("agents = %d, %v", len(agents), err)
	}
	if _, err := h.store.RevokeAgent(context.Background(), agents[0].AgentID, time.Now()); err != nil {
		t.Fatal(err)
	}

	err = openControlStream(t, h.controlAddr, h.caPEM, &cert)
	if status.Code(err) != codes.Unavailable {
		t.Errorf("吊销证书应握手失败，实际 code=%v err=%v", status.Code(err), err)
	}
}

func TestServe_AdminPortAuth(t *testing.T) {
	h := startHarness(t)

	dial := func(t *testing.T) (*grpc.ClientConn, context.Context) {
		t.Helper()
		conn, err := grpc.NewClient(h.adminAddr,
			grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{RootCAs: h.caPool()})))
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		t.Cleanup(func() { conn.Close(); cancel() })
		return conn, ctx
	}

	// admin 端口挂的是真实 EnrollmentService（见 harness RegisterAdmin），
	// 用它区分「被拦截器拒绝」与「放行到业务层」。
	t.Run("无 token 被拒", func(t *testing.T) {
		conn, ctx := dial(t)
		err := conn.Invoke(ctx, "/cagent.v1.EnrollmentService/Enroll",
			&pb.EnrollRequest{}, &pb.EnrollResponse{})
		if status.Code(err) != codes.Unauthenticated {
			t.Errorf("无 token 应 Unauthenticated，实际 code=%v err=%v", status.Code(err), err)
		}
	})

	t.Run("正确 token 放行到业务层", func(t *testing.T) {
		conn, ctx := dial(t)
		mdCtx := metadata.NewOutgoingContext(ctx,
			metadata.Pairs("authorization", "Bearer cactl_admin"))
		err := conn.Invoke(mdCtx, "/cagent.v1.EnrollmentService/Enroll",
			&pb.EnrollRequest{}, &pb.EnrollResponse{})
		if status.Code(err) != codes.InvalidArgument {
			t.Errorf("有效 token 应放行并得到业务校验 InvalidArgument，实际 code=%v err=%v", status.Code(err), err)
		}
	})
}
