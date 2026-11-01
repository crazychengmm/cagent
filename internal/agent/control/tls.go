package control

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"

	"github.com/crazychengmm/cagent/internal/agent/identity"
)

// ClientTLSConfig 用本地身份构造 mTLS 客户端配置（导出供升级等模块独立建连）。
func ClientTLSConfig(ident *identity.Identity) (*tls.Config, error) {
	return clientTLS(ident)
}

// clientTLS 用本地身份构造 mTLS 客户端配置：CA 锚点来自注册时保存的 CA。
func clientTLS(ident *identity.Identity) (*tls.Config, error) {
	if len(ident.CAPEM) == 0 {
		return nil, errors.New("本地身份缺少 server CA 锚点")
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(ident.CAPEM) {
		return nil, errors.New("本地 CA 锚点不是合法 PEM")
	}

	keyDER, err := x509.MarshalECPrivateKey(ident.Key)
	if err != nil {
		return nil, fmt.Errorf("序列化客户端私钥: %w", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	cert, err := tls.X509KeyPair(ident.CertPEM, keyPEM)
	if err != nil {
		return nil, fmt.Errorf("加载客户端证书: %w", err)
	}

	return &tls.Config{
		RootCAs:      pool,
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
		NextProtos:   []string{"h2"},
	}, nil
}
