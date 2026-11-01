package ca

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"
)

const (
	serverCertFile = "server.crt"
	serverKeyFile  = "server.key"
)

// IssueServerCertificate 由 CA 签发一张仅用于服务端认证的叶子证书，
// hosts 中可解析为 IP 的条目进 IPAddresses，其余进 DNSNames。
func (c *CA) IssueServerCertificate(hosts []string, notAfter time.Time) (certPEM, keyPEM []byte, err error) {
	if len(hosts) == 0 {
		return nil, nil, errors.New("server 证书至少需要一个 SAN")
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("生成 server 密钥: %w", err)
	}

	serial, err := NewSerial()
	if err != nil {
		return nil, nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "cagent-server"},
		NotBefore:    time.Now().Add(-clockSkewSlack),
		NotAfter:     notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	for _, host := range hosts {
		if ip := net.ParseIP(host); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else {
			tmpl.DNSNames = append(tmpl.DNSNames, host)
		}
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, c.cert, &key.PublicKey, c.key)
	if err != nil {
		return nil, nil, fmt.Errorf("签发 server 证书: %w", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, nil, fmt.Errorf("序列化 server 私钥: %w", err)
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	return certPEM, keyPEM, nil
}

// EnsureServerCertificate 从 dir 加载 server 证书；不存在则按 hosts 签发并落盘。
// 私钥文件权限 0600。轮换方式：删除两文件后重启（SAN 变更同理）。
func (c *CA) EnsureServerCertificate(dir string, hosts []string,
	validity time.Duration) (*tls.Certificate, error) {
	certPath := filepath.Join(dir, serverCertFile)
	keyPath := filepath.Join(dir, serverKeyFile)

	if _, err := os.Stat(certPath); err == nil {
		if _, err := os.Stat(keyPath); err != nil {
			return nil, fmt.Errorf("发现 server 证书但缺少私钥: %w", err)
		}
		cert, err := tls.LoadX509KeyPair(certPath, keyPath)
		if err != nil {
			return nil, fmt.Errorf("加载 server 证书: %w", err)
		}
		return &cert, nil
	}

	certPEM, keyPEM, err := c.IssueServerCertificate(hosts, time.Now().Add(validity))
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, caDirPerm); err != nil {
		return nil, fmt.Errorf("创建证书目录: %w", err)
	}
	if err := os.WriteFile(keyPath, keyPEM, caKeyPerm); err != nil {
		return nil, fmt.Errorf("写 server 私钥: %w", err)
	}
	if err := os.WriteFile(certPath, certPEM, caCertPerm); err != nil {
		return nil, fmt.Errorf("写 server 证书: %w", err)
	}

	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, fmt.Errorf("解析刚写入的 server 证书: %w", err)
	}
	return &cert, nil
}
