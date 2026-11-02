// Package ca 管理 server 内置证书机构：首启生成 ECDSA P-256 根密钥与自签 CA，
// 校验 agent CSR 并签发客户端身份证书。根私钥文件权限固定 0600。
package ca

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"time"
)

const (
	// CADirName 是数据目录下存放 CA 与服务器叶子证书的子目录。
	CADirName  = "ca"
	caCertFile = "ca.crt"
	caKeyFile  = "ca.key"

	// caKeyPerm 根私钥仅 owner 可读写。
	caKeyPerm  os.FileMode = 0o600
	caDirPerm  os.FileMode = 0o700
	caCertPerm os.FileMode = 0o644

	caValidity     = 10 * 365 * 24 * time.Hour
	clockSkewSlack = time.Minute
)

// CA 持有根证书与其签名密钥。
type CA struct {
	cert    *x509.Certificate
	certPEM []byte
	key     *ecdsa.PrivateKey
}

// LoadOrCreate 从 dir 加载 CA；不存在则生成新的 P-256 根并落盘。
func LoadOrCreate(dir string) (*CA, error) {
	certPath := filepath.Join(dir, caCertFile)
	keyPath := filepath.Join(dir, caKeyFile)

	certPEM, certErr := os.ReadFile(certPath)
	keyPEM, keyErr := os.ReadFile(keyPath)
	if certErr == nil && keyErr == nil {
		return load(certPEM, keyPEM)
	}
	if !errors.Is(certErr, os.ErrNotExist) || !errors.Is(keyErr, os.ErrNotExist) {
		return nil, fmt.Errorf("读取现有 CA 文件失败: cert=%v key=%v", certErr, keyErr)
	}
	if certErr == nil || keyErr == nil {
		return nil, errors.New("CA 证书与私钥必须同时存在，只发现其一")
	}

	return create(dir, certPath, keyPath)
}

func load(certPEM, keyPEM []byte) (*CA, error) {
	certBlock, _ := pem.Decode(certPEM)
	if certBlock == nil || certBlock.Type != "CERTIFICATE" {
		return nil, errors.New("CA 证书 PEM 解析失败")
	}
	cert, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		return nil, fmt.Errorf("解析 CA 证书: %w", err)
	}
	keyBlock, _ := pem.Decode(keyPEM)
	if keyBlock == nil || keyBlock.Type != "EC PRIVATE KEY" {
		return nil, errors.New("CA 私钥 PEM 解析失败")
	}
	key, err := x509.ParseECPrivateKey(keyBlock.Bytes)
	if err != nil {
		return nil, fmt.Errorf("解析 CA 私钥: %w", err)
	}
	return &CA{cert: cert, certPEM: certPEM, key: key}, nil
}

func create(dir, certPath, keyPath string) (*CA, error) {
	if err := os.MkdirAll(dir, caDirPerm); err != nil {
		return nil, fmt.Errorf("创建 CA 目录 %s: %w", dir, err)
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("生成 CA 密钥: %w", err)
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "cagent-server-ca"},
		NotBefore:             now.Add(-clockSkewSlack),
		NotAfter:              now.Add(caValidity),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            0,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, fmt.Errorf("自签 CA 证书: %w", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("序列化 CA 私钥: %w", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	// 先写私钥（0600），再写证书；同目录 0700。
	if err := os.WriteFile(keyPath, keyPEM, caKeyPerm); err != nil {
		return nil, fmt.Errorf("写 CA 私钥: %w", err)
	}
	if err := os.WriteFile(certPath, certPEM, caCertPerm); err != nil {
		return nil, fmt.Errorf("写 CA 证书: %w", err)
	}

	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, fmt.Errorf("解析刚签发的 CA: %w", err)
	}
	return &CA{cert: cert, certPEM: certPEM, key: key}, nil
}

// NewSerial 返回一个 128 位正序列号。
func NewSerial() (*big.Int, error) {
	limit := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(rand.Reader, limit)
	if err != nil {
		return nil, fmt.Errorf("生成序列号: %w", err)
	}
	return serial, nil
}

// SignAgentCSR 校验 CSR 自签后签发仅用于客户端认证的 agent 证书。
// CSR 只用于证明持有对应私钥；证书主体 CN 取 commonName（即 agent UUID），
// 使 mTLS 握手后可直接从对端证书识别 agent 身份。
func (c *CA) SignAgentCSR(csrPEM []byte, commonName string,
	serial *big.Int, notAfter time.Time) ([]byte, error) {
	block, _ := pem.Decode(csrPEM)
	if block == nil || block.Type != "CERTIFICATE REQUEST" {
		return nil, errors.New("CSR PEM 解析失败")
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("解析 CSR: %w", err)
	}
	if err := csr.CheckSignature(); err != nil {
		return nil, fmt.Errorf("CSR 签名无效: %w", err)
	}
	if commonName == "" {
		return nil, errors.New("证书 CN 不能为空")
	}
	if serial == nil {
		return nil, errors.New("序列号不能为空")
	}

	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:         commonName,
			OrganizationalUnit: []string{"cagent-agent"},
		},
		NotBefore:   now.Add(-clockSkewSlack),
		NotAfter:    notAfter,
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, c.cert, csr.PublicKey, c.key)
	if err != nil {
		return nil, fmt.Errorf("签发 agent 证书: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), nil
}

// CACertPEM 返回 PEM 编码的 CA 证书（随注册响应下发给 agent）。
func (c *CA) CACertPEM() []byte {
	return c.certPEM
}

// VerifyAgentCert 用 CA 校验对端证书链（供 mTLS 配置之外的显式校验/测试）。
func (c *CA) VerifyAgentCert(certPEM []byte) error {
	block, _ := pem.Decode(certPEM)
	if block == nil {
		return errors.New("证书 PEM 解析失败")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return err
	}
	pool := x509.NewCertPool()
	pool.AddCert(c.cert)
	_, err = cert.Verify(x509.VerifyOptions{
		Roots:       pool,
		KeyUsages:   []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		CurrentTime: time.Now(),
	})
	return err
}
