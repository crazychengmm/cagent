package ca

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestIssueServerCertificate_VerifiesWithSANs(t *testing.T) {
	t.Parallel()

	root, err := LoadOrCreate(filepath.Join(t.TempDir(), "ca"))
	if err != nil {
		t.Fatal(err)
	}
	certPEM, keyPEM, err := root.IssueServerCertificate(
		[]string{"cagent.example", "127.0.0.1"}, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("IssueServerCertificate: %v", err)
	}
	if _, err := tls.X509KeyPair(certPEM, keyPEM); err != nil {
		t.Fatalf("证书与私钥不配对: %v", err)
	}

	block, _ := pem.Decode(certPEM)
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if cert.IsCA {
		t.Error("server 叶子证书 IsCA 必须为 false")
	}

	pool := x509.NewCertPool()
	pool.AddCert(root.cert)
	for _, tc := range []struct {
		name string
		opts x509.VerifyOptions
	}{
		{"DNS 名校验", x509.VerifyOptions{Roots: pool, DNSName: "cagent.example"}},
		{"IP 名经 DNSName 校验", x509.VerifyOptions{Roots: pool, DNSName: "127.0.0.1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := cert.Verify(tc.opts); err != nil {
				t.Errorf("校验失败: %v", err)
			}
		})
	}
}

func TestIssueServerCertificate_RejectsEmptyHosts(t *testing.T) {
	t.Parallel()

	root, err := LoadOrCreate(filepath.Join(t.TempDir(), "ca"))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := root.IssueServerCertificate(nil, time.Now().Add(time.Hour)); err == nil {
		t.Fatal("空 SAN 应报错")
	}
}

func TestEnsureServerCertificate_GenerateLoadAndPermissions(t *testing.T) {
	t.Parallel()

	dir := filepath.Join(t.TempDir(), "ca")
	root, err := LoadOrCreate(dir)
	if err != nil {
		t.Fatal(err)
	}

	cert1, err := root.EnsureServerCertificate(dir, []string{"localhost"}, time.Hour)
	if err != nil {
		t.Fatalf("Ensure 首次: %v", err)
	}
	info, err := os.Stat(filepath.Join(dir, serverKeyFile))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != caKeyPerm {
		t.Errorf("server 私钥权限 = %o, 期望 %o", info.Mode().Perm(), caKeyPerm)
	}

	root2, err := LoadOrCreate(dir)
	if err != nil {
		t.Fatal(err)
	}
	cert2, err := root2.EnsureServerCertificate(dir, []string{"localhost"}, time.Hour)
	if err != nil {
		t.Fatalf("Ensure 二次加载: %v", err)
	}
	if string(cert1.Certificate[0]) != string(cert2.Certificate[0]) {
		t.Error("二次加载应复用既有证书，而非重新签发")
	}
}
