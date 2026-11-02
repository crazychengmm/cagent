package upgrade

import (
	"context"
	"os"
	"time"

	"github.com/crazychengmm/cagent/internal/agent/control"
	pb "github.com/crazychengmm/cagent/proto/cagent/v1"
)

const (
	// resultPollInterval 是结果文件轮询周期。
	resultPollInterval = 100 * time.Millisecond
	// idleWaitWindow 是正常连接（无升级痕迹）的短暂等待，随后即退出。
	idleWaitWindow = 500 * time.Millisecond
	// upgradeWaitWindow 是检测到升级进行中时的等待窗口，覆盖 guard 健康门控。
	// 健康门控默认 60s，留 10s 余量。
	upgradeWaitWindow = 70 * time.Second
)

// ReportPendingResults 在当前控制流上等待并上报一次升级结果。
//
// 由重启后接管的 agent 在稳定主连接上调用。结果通常在连接建立后由 guard
// 数百毫秒内写入；为避免正常连接长期挂起 goroutine，先短暂等待；若发现
// 升级现场（upgrade-state.json，guard 运行中）则延长到健康门控量级。
// 发送失败或连接结束由下一条连接重试（结果文件在成功上报前不删除）。
func ReportPendingResults(ctx context.Context, dataDir string, sender control.Sender) error {
	stateExists := func() bool {
		_, err := os.Stat(StatePath(dataDir))
		return err == nil
	}

	window := idleWaitWindow
	if stateExists() {
		window = upgradeWaitWindow
	}
	deadline := time.Now().Add(window)

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
		// 短暂窗口后若升级现场出现（guard 启动稍晚于连接），延长窗口。
		if window == idleWaitWindow && stateExists() {
			window = upgradeWaitWindow
			deadline = time.Now().Add(upgradeWaitWindow)
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
