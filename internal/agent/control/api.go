// Package control 实现 agent 到 server 的 mTLS 控制长连接：
// 心跳、断线指数退避重连、证书临期自动续签，以及下行消息的多路分发。
package control

import (
	"context"
	"log/slog"
	"time"

	"github.com/crazychengmm/cagent/internal/agent/identity"
	pb "github.com/crazychengmm/cagent/proto/cagent/v1"
)

// Sender 允许业务处理器向 server 回送消息（并发安全）。
type Sender interface {
	Send(msg *pb.AgentMessage) error
}

// JobHandler 处理下行的任务下发与取消；实现方负责并发与排队策略。
type JobHandler interface {
	OnDispatch(ctx context.Context, sender Sender, dispatch *pb.JobDispatch)
	OnCancel(jobID string)
}

// UpgradeHandler 处理下行的升级指令。
type UpgradeHandler interface {
	OnUpgrade(ctx context.Context, sender Sender, command *pb.UpgradeCommand)
}

// Config 是控制循环的全部参数。
type Config struct {
	Identity       *identity.Identity
	ServerAddr     string
	DataDir        string
	Version        string
	Labels         map[string]string
	JobHandler     JobHandler
	UpgradeHandler UpgradeHandler

	HeartbeatInterval time.Duration
	// RenewBefore 距到期不足该时长即续签。
	RenewBefore time.Duration

	// OnStateChange 在连接建立/断开时回调（观测与测试用）。
	OnStateChange func(connected bool)
	Logger        *slog.Logger
}
