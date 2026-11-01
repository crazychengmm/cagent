package ca

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func makeTestCSR(t *testing.T, commonName string) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("生成测试密钥: %v", err)
	}
	tmpl := x509.CertificateRequest{Subject: pkix.Name{CommonName: commonName}}
	der, err := x509.CreateCertificateRequest(rand.Reader, &tmpl, key)
	if err != nil {
		t.Fatalf("生成 CSR: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der})
}

func TestLoadOrCreate_FilesAndPermissions(t *testing.T) {
	t.Parallel()

	dir := filepath.Join(t.TempDir(), "ca")
	created, err := LoadOrCreate(dir)
	if err != nil {
		t.Fatalf("首次 LoadOrCreate: %v", err)
	}

	info, err := os.Stat(filepath.Join(dir, caKeyFile))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != caKeyPerm {
		t.Errorf("根私钥权限 = %o, 期望 %o", info.Mode().Perm(), caKeyPerm)
	}
	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if dirInfo.Mode().Perm() != caDirPerm {
		t.Errorf("CA 目录权限 = %o, 期望 %o", dirInfo.Mode().Perm(), caDirPerm)
	}
	certInfo, err := os.Stat(filepath.Join(dir, caCertFile))
	if err != nil {
		t.Fatal(err)
	}
	if certInfo.Mode().Perm()&0o022 != 0 {
		t.Errorf("CA 证书不应可被组/其他写入，权限 %o", certInfo.Mode().Perm())
	}

	loaded, err := LoadOrCreate(dir)
	if err != nil {
		t.Fatalf("二次 LoadOrCreate: %v", err)
	}
	if string(loaded.CACertPEM()) != string(created.CACertPEM()) {
		t.Error("重新加载得到的 CA 证书应与首次一致")
	}
}

func TestSignAgentCSR_VerifiesAgainstCA(t *testing.T) {
	t.Parallel()

	root, err := LoadOrCreate(filepath.Join(t.TempDir(), "ca"))
	if err != nil {
		t.Fatal(err)
	}
	serial, err := NewSerial()
	if err != nil {
		t.Fatal(err)
	}
	notAfter := time.Now().Add(90 * 24 * time.Hour).UTC()
	certPEM, err := root.SignAgentCSR(makeTestCSR(t, "agent-uuid-1"), serial, notAfter)
	if err != nil {
		t.Fatalf("SignAgentCSR: %v", err)
	}

	if err := root.VerifyAgentCert(certPEM); err != nil {
		t.Fatalf("签发证书应能通过 CA 校验: %v", err)
	}

	block, _ := pem.Decode(certPEM)
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if cert.IsCA {
		t.Error("agent 证书 IsCA 必须为 false")
	}
	if !cert.NotAfter.Equal(notAfter.Truncate(time.Second)) {
		// 证书时间按秒存储，容忍亚秒截断。
		if cert.NotAfter.Sub(notAfter) > time.Second || cert.NotAfter.Sub(notAfter) < -time.Second {
			t.Errorf("NotAfter = %s, 期望约 %s", cert.NotAfter, notAfter)
		}
	}
	hasClientAuth := false
	for _, eku := range cert.ExtKeyUsage {
		if eku == x509.ExtKeyUsageClientAuth {
			hasClientAuth = true
		}
	}
	if !hasClientAuth {
		t.Error("agent 证书必须包含 ClientAuth EKU")
	}
	if cert.Subject.CommonName != "agent-uuid-1" {
		t.Errorf("Subject CN = %q", cert.Subject.CommonName)
	}
}

func TestSignAgentCSR_RejectsInvalidCSR(t *testing.T) {
	t.Parallel()

	root, err := LoadOrCreate(filepath.Join(t.TempDir(), "ca"))
	if err != nil {
		t.Fatal(err)
	}
	serial, _ := NewSerial()
	if _, err := root.SignAgentCSR([]byte("not a pem"), serial, time.Now().Add(time.Hour)); err == nil {
		t.Fatal("非法 PEM 应报错")
	}
	if _, err := root.SignAgentCSR(makeTestCSR(t, "x"), nil, time.Now().Add(time.Hour)); err == nil {
		t.Fatal("空序列号应报错")
	}
}
