// Package controlservice 实现 mTLS 控制通道上的服务：
// 当前提供证书续签 RenewCert；Control 双向流与 DownloadRelease 在后续任务实现。
package controlservice

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/crazychengmm/cagent/internal/server/ca"
	"github.com/crazychengmm/cagent/internal/server/store"
	pb "github.com/crazychengmm/cagent/proto/cagent/v1"
)

// Service 是 ControlService 的 gRPC 实现。
type Service struct {
	pb.UnimplementedControlServiceServer

	store        *store.Store
	ca           *ca.CA
	certValidity time.Duration
	now          func() time.Time
	logger       *slog.Logger
}

// New 构造控制通道服务；now 为 nil 时用墙上时钟。
func New(s *store.Store, rootCA *ca.CA, certValidity time.Duration,
	now func() time.Time, logger *slog.Logger) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{store: s, ca: rootCA, certValidity: certValidity, now: now, logger: logger}
}

// RenewCert 为持有有效 mTLS 证书的 agent 续签身份证书。
func (s *Service) RenewCert(ctx context.Context,
	req *pb.RenewCertRequest) (*pb.RenewCertResponse, error) {
	agentID, err := agentIDFromContext(ctx)
	if err != nil {
		return nil, status.Error(codes.Unauthenticated, "无法从 mTLS 连接识别 agent")
	}
	if len(req.GetCsrPem()) == 0 {
		return nil, status.Error(codes.InvalidArgument, "csr_pem 不能为空")
	}

	if _, err := s.store.GetAgent(ctx, agentID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, status.Error(codes.Unauthenticated, "agent 未注册或已删除")
		}
		return nil, status.Error(codes.Internal, "查询 agent 失败")
	}

	serial, err := ca.NewSerial()
	if err != nil {
		return nil, status.Error(codes.Internal, "生成序列号失败")
	}
	notAfter := s.now().UTC().Add(s.certValidity)
	certPEM, err := s.ca.SignAgentCSR(req.GetCsrPem(), agentID, serial, notAfter)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "CSR 无效: "+err.Error())
	}

	// 旧序列号被覆盖即失效：下一条新连接必须使用新证书（吊销机同样被拒）。
	ok, err := s.store.UpdateAgentCertificate(ctx, agentID, serial.Text(16), notAfter)
	if err != nil {
		return nil, status.Error(codes.Internal, "更新证书记录失败")
	}
	if !ok {
		return nil, status.Error(codes.PermissionDenied, "agent 已吊销，拒绝续签")
	}

	s.logger.Info("agent 证书续签成功", "agent_id", agentID)
	return &pb.RenewCertResponse{
		CertificatePem: certPEM,
		NotAfter:       timestamppb.New(notAfter),
	}, nil
}

// agentIDFromContext 从 mTLS 对端证书的 CN 取 agent UUID。
func agentIDFromContext(ctx context.Context) (string, error) {
	p, ok := peer.FromContext(ctx)
	if !ok {
		return "", errors.New("上下文中无 peer 信息")
	}
	tlsInfo, ok := p.AuthInfo.(credentials.TLSInfo)
	if !ok {
		return "", errors.New("对端认证信息不是 TLS")
	}
	if len(tlsInfo.State.PeerCertificates) == 0 {
		return "", errors.New("对端未提供证书")
	}
	cn := tlsInfo.State.PeerCertificates[0].Subject.CommonName
	if cn == "" {
		return "", errors.New("对端证书 CN 为空")
	}
	return cn, nil
}
