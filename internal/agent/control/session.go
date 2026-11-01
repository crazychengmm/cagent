package control

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"sync"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/crazychengmm/cagent/internal/agent/identity"
	pb "github.com/crazychengmm/cagent/proto/cagent/v1"
)

const renewRetryInterval = time.Minute

// session 是一条已建立的 Control 双向流上的收发循环。
type session struct {
	cfg    Config
	client pb.ControlServiceClient
	stream pb.ControlService_ControlClient
	sendMu sync.Mutex
}

// Send 实现 Sender；bidi 流禁止并发 Send，加锁保护。
func (s *session) Send(msg *pb.AgentMessage) error {
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	return s.stream.Send(msg)
}

func (s *session) run(ctx context.Context) error {
	// 无缓冲：接收 goroutine 与 select 直接握手，避免在 recv 侧堆积消息。
	msgCh := make(chan *pb.ServerMessage)
	errCh := make(chan error, 1)
	go func() {
		for {
			msg, err := s.stream.Recv()
			if err != nil {
				errCh <- err
				return
			}
			select {
			case msgCh <- msg:
			case <-ctx.Done():
				return
			}
		}
	}()

	if err := s.sendHeartbeat(time.Now()); err != nil {
		return err
	}
	heartbeatTicker := time.NewTicker(s.cfg.HeartbeatInterval)
	defer heartbeatTicker.Stop()
	renewTimer := time.NewTimer(s.durationUntilRenew())
	defer renewTimer.Stop()

	for {
		select {
		case msg := <-msgCh:
			s.dispatch(ctx, msg)
		case err := <-errCh:
			return err
		case <-heartbeatTicker.C:
			if err := s.sendHeartbeat(time.Now()); err != nil {
				return err
			}
		case <-renewTimer.C:
			if err := s.renew(ctx); err != nil {
				s.cfg.Logger.Warn("证书续签失败，保留当前连接，稍后重试",
					"error", err.Error())
				renewTimer.Reset(renewRetryInterval)
			} else {
				// 本地证书已替换，主动断开以新证书重建 mTLS 连接。
				return errRenewed
			}
		case <-ctx.Done():
			_ = s.stream.CloseSend()
			return nil
		}
	}
}

func (s *session) dispatch(ctx context.Context, msg *pb.ServerMessage) {
	switch p := msg.Payload.(type) {
	case *pb.ServerMessage_HeartbeatAck:
		if p.HeartbeatAck.GetRenewCertRecommended() {
			s.cfg.Logger.Info("server 建议续签证书")
		}
	case *pb.ServerMessage_JobDispatch:
		if s.cfg.JobHandler != nil {
			go s.cfg.JobHandler.OnDispatch(ctx, s, p.JobDispatch)
		}
	case *pb.ServerMessage_JobCancel:
		if s.cfg.JobHandler != nil {
			jobID := p.JobCancel.GetJobId()
			go s.cfg.JobHandler.OnCancel(jobID)
		}
	case *pb.ServerMessage_UpgradeCommand:
		if s.cfg.UpgradeHandler != nil {
			go s.cfg.UpgradeHandler.OnUpgrade(ctx, s, p.UpgradeCommand)
		}
	default:
		s.cfg.Logger.Warn("收到未知类型的下行消息")
	}
}

func (s *session) sendHeartbeat(at time.Time) error {
	return s.Send(&pb.AgentMessage{
		Payload: &pb.AgentMessage_Heartbeat{
			Heartbeat: &pb.Heartbeat{
				AgentId:   s.cfg.Identity.AgentID,
				Version:   s.cfg.Version,
				Labels:    s.cfg.Labels,
				Timestamp: timestamppb.New(at),
			},
		},
	})
}

func (s *session) renew(ctx context.Context) error {
	csrPEM, err := identity.MakeCSR(s.cfg.Identity.Key, s.cfg.Identity.AgentID)
	if err != nil {
		return err
	}
	renewCtx, cancel := context.WithTimeout(ctx, dialTimeout)
	defer cancel()
	resp, err := s.client.RenewCert(renewCtx, &pb.RenewCertRequest{CsrPem: csrPEM})
	if err != nil {
		return err
	}
	if err := identity.ReplaceCertificate(s.cfg.DataDir, resp.GetCertificatePem()); err != nil {
		return err
	}
	// 同步内存身份，下一条连接（runOnce 重新构造 TLS）使用新证书。
	s.cfg.Identity.CertPEM = resp.GetCertificatePem()
	s.cfg.Logger.Info("证书续签成功并已原子替换",
		"not_after", resp.GetNotAfter().AsTime().Format(time.RFC3339))
	return nil
}

// durationUntilRenew 按本地证书 NotAfter 与 RenewBefore 计算续签等待时长。
func (s *session) durationUntilRenew() time.Duration {
	block, _ := pem.Decode(s.cfg.Identity.CertPEM)
	if block == nil {
		return renewRetryInterval
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return renewRetryInterval
	}
	wait := time.Until(cert.NotAfter.Add(-s.cfg.RenewBefore))
	if wait < time.Second {
		// 已临期：给连接建立与首跳留出 1 秒，避免与建连竞态。
		return time.Second
	}
	return wait
}
