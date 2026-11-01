package release

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
)

// 签名对整个 tar.gz 包计算（ed25519 内部对消息做 SHA-512）。
const (
	privateKeyPEMType = "PRIVATE KEY"
	publicKeyPEMType  = "PUBLIC KEY"
)

// GenerateSigningKey 生成一对 ed25519 发布签名密钥。
func GenerateSigningKey() (ed25519.PrivateKey, ed25519.PublicKey, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("生成签名密钥: %w", err)
	}
	return priv, pub, nil
}

// EncodePrivateKeyPEM 以 PKCS#8 PEM 序列化私钥（落盘须 0600）。
func EncodePrivateKeyPEM(priv ed25519.PrivateKey) ([]byte, error) {
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return nil, fmt.Errorf("序列化签名私钥: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: privateKeyPEMType, Bytes: der}), nil
}

// EncodePublicKeyPEM 以 SPKI PEM 序列化公钥（随安装分发到 agent）。
func EncodePublicKeyPEM(pub ed25519.PublicKey) ([]byte, error) {
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return nil, fmt.Errorf("序列化签名公钥: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: publicKeyPEMType, Bytes: der}), nil
}

// ParsePrivateKeyPEM 解析 PKCS#8 私钥。
func ParsePrivateKeyPEM(raw []byte) (ed25519.PrivateKey, error) {
	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, errors.New("私钥 PEM 解析失败")
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("解析签名私钥: %w", err)
	}
	priv, ok := key.(ed25519.PrivateKey)
	if !ok {
		return nil, errors.New("私钥不是 ed25519")
	}
	return priv, nil
}

// ParsePublicKeyPEM 解析 SPKI 公钥。
func ParsePublicKeyPEM(raw []byte) (ed25519.PublicKey, error) {
	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, errors.New("公钥 PEM 解析失败")
	}
	key, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("解析签名公钥: %w", err)
	}
	pub, ok := key.(ed25519.PublicKey)
	if !ok {
		return nil, errors.New("公钥不是 ed25519")
	}
	return pub, nil
}

// SignPackage 读取整个包并签名；ctl 离线一次性执行。
func SignPackage(priv ed25519.PrivateKey, r io.Reader) ([]byte, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("读取待签名包: %w", err)
	}
	return ed25519.Sign(priv, data), nil
}

// VerifyPackage 用公钥验签整个包。
func VerifyPackage(pub ed25519.PublicKey, packageData, signature []byte) bool {
	return ed25519.Verify(pub, packageData, signature)
}
