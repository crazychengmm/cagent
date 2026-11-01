package upgrade

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	"github.com/crazychengmm/cagent/internal/agent/control"
	"github.com/crazychengmm/cagent/internal/agent/identity"
	pb "github.com/crazychengmm/cagent/proto/cagent/v1"
)

const reporterDialWait = 10 * time.Second

// StreamReporter 经现有控制流即时上报升级事件。
type StreamReporter struct {
	Sender control.Sender
	JobID  string
}

// Report 实现 Reporter。
func (r *StreamReporter) Report(ev *pb.UpgradeEvent) {
	_ = r.Sender.Send(&pb.AgentMessage{
		Payload: &pb.AgentMessage_UpgradeEvent{UpgradeEvent: ev},
	})
}

// GuardReporter 供独立 guard 进程使用：先缓冲事件，Flush 时建一条短 mTLS
// 控制流集中上报，避免每个事件重新握手。
type GuardReporter struct {
	mu         sync.Mutex
	identity   *identity.Identity
	serverAddr string
	logger     *slog.Logger
	events     []*pb.UpgradeEvent
}

// NewGuardReporter 构造 guard 上报器。
func NewGuardReporter(ident *identity.Identity, serverAddr string, logger *slog.Logger) *GuardReporter {
	return &GuardReporter{identity: ident, serverAddr: serverAddr, logger: logger}
}

// Report 缓冲事件。
func (r *GuardReporter) Report(ev *pb.UpgradeEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, ev)
}

// Flush 建立 mTLS 控制流并发送全部缓冲事件。
func (r *GuardReporter) Flush(ctx context.Context) error {
	r.mu.Lock()
	events := append([]*pb.UpgradeEvent(nil), r.events...)
	r.mu.Unlock()
	if len(events) == 0 {
		return nil
	}
	tlsCfg, err := control.ClientTLSConfig(r.identity)
	if err != nil {
		return err
	}
	conn, err := grpc.NewClient(r.serverAddr,
		grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg)))
	if err != nil {
		return err
	}
	defer conn.Close()

	dialCtx, cancel := context.WithTimeout(ctx, reporterDialWait)
	defer cancel()
	if err := control.WaitReady(dialCtx, conn, reporterDialWait); err != nil {
		return err
	}
	stream, err := pb.NewControlServiceClient(conn).Control(ctx)
	if err != nil {
		return err
	}
	for _, ev := range events {
		if err := stream.Send(&pb.AgentMessage{
			Payload: &pb.AgentMessage_UpgradeEvent{UpgradeEvent: ev},
		}); err != nil {
			return err
		}
	}
	r.logger.Info("guard 已上报升级结果", "events", len(events))
	return stream.CloseSend()
}
