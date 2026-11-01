package identity

import (
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
)

func TestSaveLoadRoundTrip(t *testing.T) {
	t.Parallel()

	dir := filepath.Join(t.TempDir(), "data")
	key, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	certPEM := selfSignedCertPEM(t, key)
	caPEM := []byte("ca-placeholder") // CA 文件 Load 时不强制解析

	if Exists(dir) {
		t.Fatal("空目录不应存在身份")
	}
	if err := Save(dir, "ag-uuid-1", key, certPEM, caPEM); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if !Exists(dir) {
		t.Fatal("Save 后 Exists 应为 true")
	}

	ident, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if ident.AgentID != "ag-uuid-1" {
		t.Errorf("AgentID = %q", ident.AgentID)
	}
	if ident.Key.D.Cmp(key.D) != 0 || ident.Key.X.Cmp(key.X) != 0 {
		t.Error("加载的私钥与保存的不一致")
	}
	if string(ident.CertPEM) != string(certPEM) || string(ident.CAPEM) != "ca-placeholder" {
		t.Errorf("证书/CA 内容不符")
	}
}

func TestSave_Permissions(t *testing.T) {
	t.Parallel()

	dir := filepath.Join(t.TempDir(), "data")
	key, _ := GenerateKey()
	if err := Save(dir, "ag-1", key, []byte("c"), []byte("ca")); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != dirPerm {
		t.Errorf("目录权限 = %o, 期望 %o", info.Mode().Perm(), dirPerm)
	}
	for _, tc := range []struct {
		name string
		perm os.FileMode
	}{
		{"identity", filePerm}, {"agent.key", filePerm}, {"agent.crt", filePerm},
	} {
		info, err := os.Stat(filepath.Join(dir, tc.name))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm()&0o077 != 0 {
			t.Errorf("%s 权限过宽 = %o", tc.name, info.Mode().Perm())
		}
	}
}

func TestSave_IncompleteRejected(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	key, _ := GenerateKey()
	if err := Save(filepath.Join(dir, "d"), "", key, []byte("c"), nil); err == nil {
		t.Error("空 AgentID 应报错")
	}
	if err := Save(filepath.Join(dir, "d"), "ag", nil, []byte("c"), nil); err == nil {
		t.Error("nil 私钥应报错")
	}
	if err := Save(filepath.Join(dir, "d"), "ag", key, nil, nil); err == nil {
		t.Error("空证书应报错")
	}
	if err := Save(filepath.Join(dir, "d"), "ag", key, []byte("not-pem"), nil); err != nil {
		t.Errorf("Save 不校验证书内容（由注册流程保证），应保存成功: %v", err)
	}
}

func TestLoad_MissingFiles(t *testing.T) {
	t.Parallel()

	if _, err := Load(t.TempDir()); err == nil {
		t.Fatal("无身份目录 Load 应报错")
	}
}

func TestReplaceCertificate(t *testing.T) {
	t.Parallel()

	dir := filepath.Join(t.TempDir(), "data")
	key, _ := GenerateKey()
	csrPEM, err := MakeCSR(key, "host1")
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(csrPEM)
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if err := csr.CheckSignature(); err != nil {
		t.Errorf("CSR 自签校验失败: %v", err)
	}

	if err := Save(dir, "ag-1", key, []byte("old"), nil); err != nil {
		t.Fatal(err)
	}
	if err := ReplaceCertificate(dir, []byte("not a certificate")); err == nil {
		t.Fatal("非法证书不应允许替换")
	}
	// 用 CSR 不是证书——构造一个最小自签证书验证合法路径。
	selfSignedPEM := selfSignedCertPEM(t, key)
	if err := ReplaceCertificate(dir, selfSignedPEM); err != nil {
		t.Fatalf("ReplaceCertificate: %v", err)
	}
	ident, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if string(ident.CertPEM) != string(selfSignedPEM) {
		t.Error("替换后应加载到新证书")
	}
}
