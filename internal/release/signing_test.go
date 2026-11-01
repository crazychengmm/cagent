package release

import (
	"bytes"
	"crypto/ed25519"
	"strings"
	"testing"
)

func TestSigningKeys_PEMRoundTrip(t *testing.T) {
	t.Parallel()

	priv, pub, err := GenerateSigningKey()
	if err != nil {
		t.Fatal(err)
	}
	privPEM, err := EncodePrivateKeyPEM(priv)
	if err != nil {
		t.Fatal(err)
	}
	pubPEM, err := EncodePublicKeyPEM(pub)
	if err != nil {
		t.Fatal(err)
	}
	priv2, err := ParsePrivateKeyPEM(privPEM)
	if err != nil {
		t.Fatalf("解析私钥: %v", err)
	}
	pub2, err := ParsePublicKeyPEM(pubPEM)
	if err != nil {
		t.Fatalf("解析公钥: %v", err)
	}
	if !priv2.Equal(priv) || !pub2.Equal(pub) {
		t.Error("PEM 往返后密钥不一致")
	}
}

func TestSignAndVerify(t *testing.T) {
	t.Parallel()

	priv, pub, _ := GenerateSigningKey()
	pkg := bytes.Repeat([]byte("release-binary"), 1000)

	sig, err := SignPackage(priv, bytes.NewReader(pkg))
	if err != nil {
		t.Fatal(err)
	}
	if len(sig) != ed25519.SignatureSize {
		t.Errorf("签名长度 = %d", len(sig))
	}
	if !VerifyPackage(pub, pkg, sig) {
		t.Error("合法签名应验证通过")
	}

	tampered := append([]byte(nil), pkg...)
	tampered[0] ^= 0xff
	if VerifyPackage(pub, tampered, sig) {
		t.Error("篡改包必须验签失败")
	}

	sig[0] ^= 0xff
	if VerifyPackage(pub, pkg, sig) {
		t.Error("篡改签名必须验签失败")
	}

	_, other, _ := GenerateSigningKey()
	sig[0] ^= 0xff // 还原签名
	if VerifyPackage(other, pkg, sig) {
		t.Error("他人公钥不应验证通过")
	}
}

func TestParseRejectsGarbage(t *testing.T) {
	t.Parallel()

	if _, err := ParsePrivateKeyPEM([]byte("nope")); err == nil {
		t.Error("垃圾私钥应报错")
	}
	if _, err := ParsePublicKeyPEM([]byte("nope")); err == nil {
		t.Error("垃圾公钥应报错")
	}
	priv, _, _ := GenerateSigningKey()
	privPEM, _ := EncodePrivateKeyPEM(priv)
	if !strings.Contains(string(privPEM), "PRIVATE KEY") {
		t.Error("私钥 PEM 类型错误")
	}
}
