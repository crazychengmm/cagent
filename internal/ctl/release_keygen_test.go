package ctl

import (
	"bytes"
	"crypto/ed25519"
	"os"
	"path/filepath"
	"testing"

	"github.com/crazychengmm/cagent/internal/release"
)

func ed25519Sign(priv ed25519.PrivateKey, data []byte) []byte {
	return ed25519.Sign(priv, data)
}

func TestReleaseKeygen(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	key := filepath.Join(dir, "release.key")
	pub := filepath.Join(dir, "release.pub")
	var out, errBuf bytes.Buffer

	code := Run([]string{"release", "keygen", "--key", key, "--pub", pub}, &out, &errBuf)
	if code != 0 {
		t.Fatalf("keygen 退出码=%d err=%s", code, errBuf.String())
	}
	if _, err := os.Stat(key); err != nil {
		t.Fatalf("私钥未生成: %v", err)
	}
	if info, _ := os.Stat(key); info.Mode().Perm() != 0o600 {
		t.Errorf("私钥权限=%o，期望 0600", info.Mode().Perm())
	}
	if _, err := os.Stat(pub); err != nil {
		t.Fatalf("公钥未生成: %v", err)
	}

	// 生成的密钥对应可用于验签
	keyPEM, _ := os.ReadFile(key)
	pubPEM, _ := os.ReadFile(pub)
	priv, err := release.ParsePrivateKeyPEM(keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	public, err := release.ParsePublicKeyPEM(pubPEM)
	if err != nil {
		t.Fatal(err)
	}
	sig := ed25519Sign(priv, []byte("data"))
	if !release.VerifyPackage(public, []byte("data"), sig) {
		t.Fatal("生成的密钥对无法验签")
	}
	if !bytes.Contains(out.Bytes(), []byte("release.key")) {
		t.Errorf("输出未提示私钥路径: %s", out.String())
	}
}

func TestReleaseKeygen_NoOverwrite(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	key := filepath.Join(dir, "k")
	pub := filepath.Join(dir, "p")
	if code := Run([]string{"release", "keygen", "--key", key, "--pub", pub},
		&bytes.Buffer{}, &bytes.Buffer{}); code != 0 {
		t.Fatalf("首次生成失败 code=%d", code)
	}
	var errBuf bytes.Buffer
	if code := Run([]string{"release", "keygen", "--key", key, "--pub", pub},
		&bytes.Buffer{}, &errBuf); code != 1 {
		t.Errorf("覆盖已存在密钥应拒绝 code=%d", code)
	}
	if code := Run([]string{"release", "keygen", "--key", key, "--pub", pub, "--force"},
		&bytes.Buffer{}, &bytes.Buffer{}); code != 0 {
		t.Errorf("--force 应允许覆盖 code=%d", code)
	}
}
