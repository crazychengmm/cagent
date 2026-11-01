// Package tlsconfig 构造 enrollment（单向 TLS）与 control（mTLS）两端口的
// TLS 配置；control 端口在建连时强制校验客户端证书链并查询吊销状态。
package tlsconfig

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const revocationCheckTimeout = 3 * time.Second

// 握手阶段的可预期错误。
var (
	// ErrPeerRevoked 表示对端证书属于已被吊销的 agent。
	ErrPeerRevoked = errors.New("对端证书已吊销")
	// ErrNoPeerCertificate 表示对端未提供客户端证书。
	ErrNoPeerCertificate = errors.New("对端未提供客户端证书")
)

// Enrollment 返回注册端口配置：仅服务端出示证书，客户端凭 CA 锚点校验。
func Enrollment(serverCert tls.Certificate) *tls.Config {
	return &tls.Config{
		Certificates: []tls.Certificate{serverCert},
		MinVersion:   tls.VersionTLS12,
		NextProtos:   []string{"h2"},
	}
}

// RevocationChecker 按证书序列号（十六进制）判断证书是否仍有效。
type RevocationChecker func(ctx context.Context, serialHex string) (bool, error)

// Control 返回控制端口配置：强制并校验客户端证书，链由 caPEM 验证，
// 另经 checker 查吊销表；证书链无效或已吊销都会导致握手失败。
func Control(serverCert tls.Certificate, caPEM []byte, checker RevocationChecker) (*tls.Config, error) {
	if checker == nil {
		return nil, errors.New("吊销检查器不能为空")
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, errors.New("CA PEM 解析失败")
	}

	cfg := &tls.Config{
		Certificates: []tls.Certificate{serverCert},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    pool,
		MinVersion:   tls.VersionTLS12,
		NextProtos:   []string{"h2"},
	}
	cfg.VerifyConnection = func(state tls.ConnectionState) error {
		if len(state.PeerCertificates) == 0 {
			return ErrNoPeerCertificate
		}
		serial := state.PeerCertificates[0].SerialNumber.Text(16)

		ctx, cancel := context.WithTimeout(context.Background(), revocationCheckTimeout)
		defer cancel()
		active, err := checker(ctx, serial)
		if err != nil {
			return status.Errorf(codes.Unavailable, "证书吊销状态查询失败: %v", err)
		}
		if !active {
			return fmt.Errorf("%w: serial=%s", ErrPeerRevoked, serial)
		}
		return nil
	}
	return cfg, nil
}
