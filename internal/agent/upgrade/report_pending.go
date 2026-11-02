package upgrade

import (
	"context"
	"time"

	"github.com/crazychengmm/cagent/internal/agent/control"
	pb "github.com/crazychengmm/cagent/proto/cagent/v1"
)

const (
	// resultPollInterval 是结果文件轮询周期。
	resultPollInterval = 100 * time.Millisecond
	// resultWaitWindow 是连接建立后等待 guard 落结果的窗口：
	// 新 agent 重连通常早于 guard 完成健康门控（二者并行重启）。
	resultWaitWindow = 70 * time.Second
)

// ReportPendingResults 在当前控制流上等待并上报一次升级结果。
// 与连接生命周期一致：结果可能在连接建立后才落盘，故先等待一个窗口；
// 发送失败或连接结束则由下一条连接重试（结果文件在成功上报前不删除）。
func ReportPendingResults(ctx context.Context, dataDir string, sender control.Sender) error {
	deadline := time.Now().Add(resultWaitWindow)
	for {
		pending, err := LoadResult(dataDir)
		if err == nil && pending != nil {
			msg := &pb.AgentMessage{
				Payload: &pb.AgentMessage_UpgradeEvent{UpgradeEvent: pending.ToEvent()},
			}
			if err := sender.Send(msg); err != nil {
				return err
			}
			return ClearResult(dataDir)
		}
		if !time.Now().Before(deadline) {
			return nil // 本次连接无升级结果，正常退出
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(resultPollInterval):
		}
	}
}
