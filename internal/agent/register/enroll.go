// Package register 实现 agent 首次向 server 的注册调用（单向 TLS + token）。
package register

import (
	"context"
	"crypto/ecdsa"
	"crypto/tls"
	"errors"
	"fmt"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"

	"github.com/crazychengmm/cagent/internal/agent/identity"
	pb "github.com/crazychengmm/cagent/proto/cagent/v1"
)

// Result 是一次成功注册返回并需要本地持久化的全部材料。
type Result struct {
	AgentID  string
	CertPEM  []byte
	CAPEM    []byte
	NotAfter time.Time
}

// Enroll 使用锚点 TLS 配置连接 enrollment 端口，提交 CSR 与引导令牌完成注册。
// 锚点不匹配时在 TLS 握手阶段失败，enrollment token 不会离开本机。
func Enroll(ctx context.Context, enrollmentAddr string, tlsCfg *tls.Config,
	token, hostname string, labels map[string]string, key *ecdsa.PrivateKey) (*Result, error) {
	if tlsCfg == nil {
		return nil, errors.New("注册必须提供带信任锚点的 TLS 配置")
	}
	if token == "" {
		return nil, errors.New("enrollment token 不能为空")
	}

	csrPEM, err := identity.MakeCSR(key, hostname)
	if err != nil {
		return nil, err
	}

	conn, err := grpc.NewClient(enrollmentAddr,
		grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg)))
	if err != nil {
		return nil, fmt.Errorf("连接 enrollment 服务: %w", err)
	}
	defer conn.Close()

	resp, err := pb.NewEnrollmentServiceClient(conn).Enroll(ctx, &pb.EnrollRequest{
		EnrollmentToken: token,
		CsrPem:          csrPEM,
		Hostname:        hostname,
		Labels:          labels,
	})
	if err != nil {
		return nil, mapEnrollError(err)
	}

	return &Result{
		AgentID:  resp.GetAgentId(),
		CertPEM:  resp.GetCertificatePem(),
		CAPEM:    resp.GetCaCertificatePem(),
		NotAfter: resp.GetNotAfter().AsTime(),
	}, nil
}

func mapEnrollError(err error) error {
	switch status.Code(err) {
	case codes.PermissionDenied:
		return fmt.Errorf("注册被拒绝（token 无效、过期或已吊销）: %w", err)
	case codes.InvalidArgument:
		return fmt.Errorf("注册请求被拒: %w", err)
	default:
		return fmt.Errorf("注册失败: %w", err)
	}
}
