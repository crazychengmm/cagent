package cli

import (
	"context"
	"io"
	"os"
	"os/signal"
	"syscall"

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
	logger.Info("guard 进程启动", "state", statePath, "config", configPath, "pid", os.Getpid())

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
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// guard 自身已在独立 cgroup（systemd-run unit），回滚重启可同步执行。
	installer := agentupgrade.NewSystemInstaller(state,
		agentupgrade.NewRollbackRestarter(state.Unit, state.RestartCommand))
	probe := &agentupgrade.FileHealthProbe{
		DataDir:         cfg.DataDir,
		ExpectedVersion: state.ToVersion,
	}
	deps := agentupgrade.Deps{
		Installer:     installer,
		Probe:         probe,
		HealthTimeout: agentupgrade.DefaultHealthTimeout,
	}

	cmd := &pb.UpgradeCommand{JobId: state.JobID, Version: state.ToVersion}
	logger.Info("guard 开始健康门控", "expect", state.ToVersion, "data_dir", cfg.DataDir, "timeout", agentupgrade.DefaultHealthTimeout)
	result := agentupgrade.GuardWatch(ctx, cmd, deps, state.FromVersion, state.JobID)
	logger.Info("guard 门控结束", "state", result.State, "reason", result.Reason)

	// 结果落盘，由重启后接管的 agent 通过其稳定主连接上报（避免短连接竞争）。
	status := string(result.State)
	if result.State == agentupgrade.StateRolledBack {
		status = "rolled_back"
	}
	pending := &agentupgrade.PendingResult{
		JobID:       state.JobID,
		FromVersion: state.FromVersion,
		ToVersion:   state.ToVersion,
		Status:      status,
		Message:     result.Reason,
	}
	if err := agentupgrade.SaveResult(cfg.DataDir, pending); err != nil {
		logger.Error("guard 写升级结果失败", "error", err.Error())
	}

	if result.State == agentupgrade.StateSucceeded {
		logger.Info("升级健康门控通过", "version", state.ToVersion)
	} else {
		logger.Warn("升级已回滚", "version", state.ToVersion, "reason", result.Reason)
	}
	_ = agentupgrade.ClearState(agentupgrade.StatePath(cfg.DataDir))
	agentupgrade.ClearHealthy(cfg.DataDir)
	return exitOK
}
