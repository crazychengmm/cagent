// Package trust 构造 agent 首次注册时对 server 的信任锚点。
// 只接受两种锚点：CA 证书 PEM 文件，或服务器证书的 SHA256 指纹；
// 不提供也不允许任何跳过校验的开关（无 --insecure）。
package trust

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
)

// 锚点错误。
var (
	// ErrNoAnchor 表示未配置任何信任锚点。
	ErrNoAnchor = errors.New("必须配置 server CA 证书文件或证书指纹作为信任锚点")
	// ErrFingerprintMismatch 表示服务器证书与配置指纹不符。
	ErrFingerprintMismatch = errors.New("服务器证书指纹与锚点不匹配")
)

// EnrollmentTLS 按锚点构造注册连接的 TLS 配置；两种锚点都为空即报错，
// 从构造阶段杜绝「不校验 server」的连接。
func EnrollmentTLS(caFile, fingerprint string) (*tls.Config, error) {
	switch {
	case caFile != "":
		pem, err := os.ReadFile(caFile)
		if err != nil {
			return nil, fmt.Errorf("读取 server CA 文件 %s: %w", caFile, err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("server CA 文件 %s 内容不是合法 PEM 证书", caFile)
		}
		return &tls.Config{
			RootCAs:    pool,
			MinVersion: tls.VersionTLS12,
			NextProtos: []string{"h2"},
		}, nil
	case fingerprint != "":
		want, err := normalizeFingerprint(fingerprint)
		if err != nil {
			return nil, err
		}
		// pin 模式下证书链不经过系统/PEM 信任，只比对整张服务器证书指纹。
		cfg := &tls.Config{
			InsecureSkipVerify: true, //nolint:gosec // 校验改由 VerifyConnection 指纹比对完成
			MinVersion:         tls.VersionTLS12,
			NextProtos:         []string{"h2"},
		}
		cfg.VerifyConnection = func(state tls.ConnectionState) error {
			if len(state.PeerCertificates) == 0 {
				return errors.New("服务器未提供证书")
			}
			sum := sha256.Sum256(state.PeerCertificates[0].Raw)
			if hex.EncodeToString(sum[:]) != want {
				return fmt.Errorf("%w: 期望 %s", ErrFingerprintMismatch, want)
			}
			return nil
		}
		return cfg, nil
	default:
		return nil, ErrNoAnchor
	}
}

// MTLSConfig 在锚点 TLS 之上叠加 agent 客户端证书，用于注册后的控制连接。
func MTLSConfig(base *tls.Config, cert tls.Certificate) *tls.Config {
	cfg := base.Clone()
	cfg.Certificates = []tls.Certificate{cert}
	return cfg
}

func normalizeFingerprint(raw string) (string, error) {
	clean := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(raw), ":", ""))
	if decoded, err := hex.DecodeString(clean); err != nil || len(decoded) != sha256.Size {
		return "", fmt.Errorf("证书指纹必须是 %d 字节的十六进制（SHA256）", sha256.Size)
	}
	return clean, nil
}
