package cli

import (
	"context"
	"io"
	"os/signal"
	"syscall"
	"time"

	"github.com/crazychengmm/cagent/internal/agent/identity"
	agentupgrade "github.com/crazychengmm/cagent/internal/agent/upgrade"
	"github.com/crazychengmm/cagent/internal/config"
	"github.com/crazychengmm/cagent/internal/logging"
	pb "github.com/crazychengmm/cagent/proto/cagent/v1"
)

// markHealthyOnConnect 返回连接状态回调：新版本首次建立 mTLS 控制流时
// 写入健康标记，供独立 guard 进程判定健康门控通过。
func markHealthyOnConnect(dataDir, version string) func(bool) {
	return func(connected bool) {
		if connected {
			_ = agentupgrade.MarkHealthy(dataDir, version)
		}
	}
}

// runGuardMode 是升级 guard 的独立进程入口（由主进程 detach 启动）。
func runGuardMode(statePath, configPath, logLevel string, stderr io.Writer) int {
	logger, err := logging.New(logLevel, stderr)
	if err != nil {
		_, _ = stderr.Write([]byte(err.Error() + "\n"))
		return exitError
	}

	cfg, err := config.LoadAgentConfig(configPath)
	if err != nil {
		logger.Error("guard 读取配置失败", "error", err.Error())
		return exitError
	}
	state, err := agentupgrade.LoadState(statePath)
	if err != nil {
		logger.Error("guard 读取升级状态失败", "error", err.Error())
		return exitError
	}
	ident, err := identity.Load(cfg.DataDir)
	if err != nil {
		logger.Error("guard 读取身份失败", "error", err.Error())
		return exitError
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	reporter := agentupgrade.NewGuardReporter(ident, cfg.ControlAddress, logger)
	// guard 自己的 Restart 只执行 systemctl（不再 spawn 新 guard）。
	installer := agentupgrade.NewSystemInstaller(state,
		agentupgrade.NewSystemdRestarter(state.Unit))
	probe := &agentupgrade.FileHealthProbe{
		DataDir:         cfg.DataDir,
		ExpectedVersion: state.ToVersion,
	}
	deps := agentupgrade.Deps{
		Installer:     installer,
		Probe:         probe,
		Reporter:      reporter,
		HealthTimeout: agentupgrade.DefaultHealthTimeout,
	}

	cmd := &pb.UpgradeCommand{JobId: state.JobID, Version: state.ToVersion}
	result := agentupgrade.GuardWatch(ctx, cmd, deps, state.FromVersion, state.JobID)

	flushCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := reporter.Flush(flushCtx); err != nil {
		logger.Error("guard 上报结果失败", "error", err.Error())
	}

	if result.State == agentupgrade.StateSucceeded {
		logger.Info("升级健康门控通过", "version", state.ToVersion)
	} else {
		logger.Warn("升级已回滚", "version", state.ToVersion, "reason", result.Reason)
	}
	_ = agentupgrade.ClearState(statePath)
	agentupgrade.ClearHealthy(cfg.DataDir)
	return exitOK
}
