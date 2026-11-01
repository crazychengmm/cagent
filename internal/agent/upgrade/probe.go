package upgrade

import (
	"context"
	"time"
)

// healthPollInterval 是健康标记轮询周期。
const healthPollInterval = 200 * time.Millisecond

// FileHealthProbe 通过共享数据目录中的 healthy 标记判定新版本健康；
// 新 agent 首次心跳成功后写入标记。
type FileHealthProbe struct {
	DataDir         string
	ExpectedVersion string
}

// WaitHealthy 阻塞直到健康标记出现、超时或 ctx 取消。
func (p *FileHealthProbe) WaitHealthy(ctx context.Context, timeout time.Duration) error {
	if p.healthy() {
		return nil
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(healthPollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if p.healthy() {
				return nil
			}
		case <-deadline.C:
			return ErrHealthTimeout
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (p *FileHealthProbe) healthy() bool {
	return IsHealthy(p.DataDir, p.ExpectedVersion)
}

// ErrHealthTimeout 表示新版本未在窗口内上报健康。
var ErrHealthTimeout = healthTimeoutError{}

type healthTimeoutError struct{}

func (healthTimeoutError) Error() string { return "新版本未在健康窗口内上报心跳" }
