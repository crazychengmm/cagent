package upgrade

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// probeTrace 将健康门控判定细节追加到 dataDir/health-probe.log，
// 供独立 guard 进程（systemd-run unit）升级排障使用。
func probeTrace(dataDir, format string, a ...any) {
	f, err := os.OpenFile(filepath.Join(dataDir, "health-probe.log"),
		os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s "+format+"\n", append([]any{time.Now().Format("15:04:05.000")}, a...)...)
}

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
	marker := filepath.Join(p.DataDir, healthyFileName)
	probeTrace(p.DataDir, "guard pid=%d 开始健康门控 expect=%q", os.Getpid(), p.ExpectedVersion)
	deadline := time.Now().Add(timeout)
	var last string
	for {
		if raw, err := os.ReadFile(marker); err == nil {
			last = string(raw)
		} else {
			last = "<none>"
		}
		if p.healthy() {
			probeTrace(p.DataDir, "健康门控通过 expect=%q", p.ExpectedVersion)
			return nil
		}
		if !time.Now().Before(deadline) {
			probeTrace(p.DataDir, "健康门控超时 expect=%q last=%q", p.ExpectedVersion, last)
			return ErrHealthTimeout
		}
		if ctx.Err() != nil {
			probeTrace(p.DataDir, "健康门控 ctx 取消: %v", ctx.Err())
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
