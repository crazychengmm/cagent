package upgrade

import (
	"context"
	"time"
)

// healthPollInterval 是健康标记轮询间隔。
// 取值较短：独立 guard 进程（Setsid detach）中较长的单次 Sleep/timer
// 在与服务重启信号并发时存在不可靠现象，密集轮询经 e2e 压测稳定；
// 升级窗口内短暂的文件轮询开销可忽略。
const healthPollInterval = 20 * time.Millisecond

// FileHealthProbe 通过共享数据目录中的 healthy 标记判定新版本健康；
// 新 agent 首次 mTLS 心跳成功后写入标记。
type FileHealthProbe struct {
	DataDir         string
	ExpectedVersion string
}

// WaitHealthy 阻塞直到健康标记出现、超时或 ctx 取消。
func (p *FileHealthProbe) WaitHealthy(ctx context.Context, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		if p.healthy() {
			return nil
		}
		if !time.Now().Before(deadline) {
			return ErrHealthTimeout
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		time.Sleep(healthPollInterval)
	}
}

func (p *FileHealthProbe) healthy() bool {
	return IsHealthy(p.DataDir, p.ExpectedVersion)
}

// ErrHealthTimeout 表示新版本未在健康窗口内上报健康。
var ErrHealthTimeout = healthTimeoutError{}

type healthTimeoutError struct{}

func (healthTimeoutError) Error() string { return "新版本未在健康窗口内上报心跳" }
