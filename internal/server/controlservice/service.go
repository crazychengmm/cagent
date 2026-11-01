// Package controlservice 实现 mTLS 控制通道上的服务：
// 当前提供证书续签 RenewCert；Control 双向流与 DownloadRelease 在后续任务实现。
package controlservice

import (
	"context"
	"crypto/x509"
	"errors"
	"io"
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

// renewHintBefore 是 server 通过 HeartbeatAck 建议续签的提前量。
const renewHintBefore = 30 * 24 * time.Hour

// ErrAgentOffline 表示该 agent 当前没有活跃控制流。
var ErrAgentOffline = errors.New("agent 不在线")

// Service 是 ControlService 的 gRPC 实现。
type Service struct {
	pb.UnimplementedControlServiceServer

	store        *store.Store
	ca           *ca.CA
	certValidity time.Duration
	now          func() time.Time
	logger       *slog.Logger
	registry     *streamRegistry
}

// New 构造控制通道服务；now 为 nil 时用墙上时钟。
func New(s *store.Store, rootCA *ca.CA, certValidity time.Duration,
	now func() time.Time, logger *slog.Logger) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{
		store:        s,
		ca:           rootCA,
		certValidity: certValidity,
		now:          now,
		logger:       logger,
		registry:     newStreamRegistry(),
	}
}

// OnlineAgentIDs 返回当前有活跃控制流的 agent 快照。
func (s *Service) OnlineAgentIDs() []string {
	return s.registry.onlineAgentIDs()
}

// SendToAgent 向指定 agent 的当前控制流下发一条消息；离线返回 ErrAgentOffline。
func (s *Service) SendToAgent(agentID string, msg *pb.ServerMessage) error {
	session, ok := s.registry.get(agentID)
	if !ok {
		return ErrAgentOffline
	}
	return session.send(msg)
}

// Control 承载一条 agent 控制流：接收心跳/事件，下行 ack 与指令。
func (s *Service) Control(stream pb.ControlService_ControlServer) error {
	agentID, err := agentIDFromContext(stream.Context())
	if err != nil {
		return status.Error(codes.Unauthenticated, "无法从 mTLS 连接识别 agent")
	}
	ctx, cancel := context.WithCancel(stream.Context())
	defer cancel()

	session := &streamSession{agentID: agentID, stream: stream, cancel: cancel}
	if old := s.registry.register(session); old != nil {
		// 同一 agent 重连：关闭旧流，避免僵尸连接占用注册项。
		s.logger.Info("agent 重连，关闭旧控制流", "agent_id", agentID)
		old.cancel()
	}
	defer s.registry.remove(agentID, session)

	peerCert := peerCertificate(stream.Context())
	s.logger.Info("agent 控制流建立", "agent_id", agentID)
	for {
		msg, err := stream.Recv()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		switch p := msg.Payload.(type) {
		case *pb.AgentMessage_Heartbeat:
			if err := s.handleHeartbeat(ctx, session, p.Heartbeat, peerCert); err != nil {
				return err
			}
		case *pb.AgentMessage_JobEvent:
			s.logger.Debug("收到 JobEvent（由任务组处理）", "job_id", p.JobEvent.GetJobId())
		case *pb.AgentMessage_UpgradeEvent:
			s.logger.Debug("收到 UpgradeEvent（由升级组处理）", "job_id", p.UpgradeEvent.GetJobId())
		default:
			s.logger.Warn("收到未知类型的 AgentMessage", "agent_id", agentID)
		}
	}
}

func (s *Service) handleHeartbeat(ctx context.Context, session *streamSession,
	hb *pb.Heartbeat, peerCert *x509.Certificate) error {
	now := s.now().UTC()
	// 吊销/删除的 agent：心跳落库失败即终止流（吊销在长连接上即时生效）。
	err := s.store.RecordHeartbeat(ctx, session.agentID, hb.GetVersion(), hb.GetLabels(), now)
	if errors.Is(err, store.ErrNotFound) {
		return status.Error(codes.PermissionDenied, "agent 未注册或已吊销")
	}
	if err != nil {
		return status.Error(codes.Internal, "记录心跳失败")
	}

	hint := peerCert != nil && peerCert.NotAfter.Before(now.Add(renewHintBefore))
	return session.send(&pb.ServerMessage{
		Payload: &pb.ServerMessage_HeartbeatAck{
			HeartbeatAck: &pb.HeartbeatAck{
				ServerTime:           timestamppb.New(now),
				RenewCertRecommended: hint,
			},
		},
	})
}

// peerCertificate 从 mTLS 上下文中取对端叶子证书。
func peerCertificate(ctx context.Context) *x509.Certificate {
	p, ok := peer.FromContext(ctx)
	if !ok {
		return nil
	}
	tlsInfo, ok := p.AuthInfo.(credentials.TLSInfo)
	if !ok || len(tlsInfo.State.PeerCertificates) == 0 {
		return nil
	}
	return tlsInfo.State.PeerCertificates[0]
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
