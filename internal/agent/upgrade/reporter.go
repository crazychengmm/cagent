package upgrade

import (
	"github.com/crazychengmm/cagent/internal/agent/control"
	pb "github.com/crazychengmm/cagent/proto/cagent/v1"
)

// StreamReporter 经现有控制流即时上报升级事件（INSTALLING/重启前失败）。
// 最终结果（succeeded/rolled_back/failed）由 guard 写入结果文件，
// 重启后的 agent 主连接经 ReportPendingResults 上报，避免短连接竞争。
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
