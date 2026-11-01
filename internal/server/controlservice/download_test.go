package controlservice

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"

	"github.com/crazychengmm/cagent/internal/agent/identity"
	"github.com/crazychengmm/cagent/internal/server/store"
	pb "github.com/crazychengmm/cagent/proto/cagent/v1"
)

func dialControlClient(t *testing.T, h *controlHarness, ident *identity.Identity) pb.ControlServiceClient {
	t.Helper()
	keyDER, err := x509.MarshalPKCS8PrivateKey(ident.Key)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	cert, err := tls.X509KeyPair(ident.CertPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(h.rootCA.CACertPEM())
	conn, err := grpc.NewClient(h.addr, grpc.WithTransportCredentials(
		credentials.NewTLS(&tls.Config{RootCAs: pool, Certificates: []tls.Certificate{cert}})))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return pb.NewControlServiceClient(conn)
}

func publishReleaseFile(t *testing.T, h *controlHarness, version string,
	content []byte, withFile bool) {
	t.Helper()
	now := time.Now().UTC()
	if err := h.store.InsertRelease(context.Background(), store.Release{
		Version: version, Size: int64(len(content)), SHA256Hex: "x",
		Signature: []byte{}, UploadedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if withFile {
		if err := os.WriteFile(filepath.Join(h.pkgDir, version+".pkg"), content, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func downloadAll(t *testing.T, client pb.ControlServiceClient, version string) ([]byte, error) {
	t.Helper()
	stream, err := client.DownloadRelease(context.Background(),
		&pb.DownloadReleaseRequest{Version: version})
	if err != nil {
		return nil, err
	}
	var data []byte
	for {
		chunk, err := stream.Recv()
		if err == io.EOF {
			return data, nil
		}
		if err != nil {
			return nil, err
		}
		data = append(data, chunk.GetData()...)
	}
}

func TestDownloadRelease_Success(t *testing.T) {
	h := startControlHarness(t)
	defer h.stop()

	content := []byte("release-package-bytes-0123456789")
	publishReleaseFile(t, h, "1.0.0", content, true)
	ident := h.provisionAgent(t, "ag-dl")
	client := dialControlClient(t, h, ident)

	got, err := downloadAll(t, client, "1.0.0")
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	if string(got) != string(content) {
		t.Errorf("下载内容不符: %q", got)
	}
}

func TestDownloadRelease_UnknownVersion(t *testing.T) {
	h := startControlHarness(t)
	defer h.stop()

	ident := h.provisionAgent(t, "ag-dl2")
	client := dialControlClient(t, h, ident)
	_, err := downloadAll(t, client, "9.9.9")
	if status.Code(err) != codes.NotFound {
		t.Errorf("未知版本应 NotFound，实际 %v", err)
	}
}

func TestDownloadRelease_MissingFile(t *testing.T) {
	h := startControlHarness(t)
	defer h.stop()

	publishReleaseFile(t, h, "1.0.1", []byte("x"), false)
	ident := h.provisionAgent(t, "ag-dl3")
	client := dialControlClient(t, h, ident)
	_, err := downloadAll(t, client, "1.0.1")
	if status.Code(err) != codes.NotFound {
		t.Errorf("缺文件应 NotFound，实际 %v", err)
	}
}
