// Package cli 实现 cagent 二进制的子命令分发（agent/server/ctl）。
// 业务运行逻辑在后续任务接入，本包只负责参数解析、配置加载与日志接线。
package cli

import (
	"flag"
	"fmt"
	"io"

	"github.com/crazychengmm/cagent/internal/config"
	"github.com/crazychengmm/cagent/internal/logging"
	"github.com/crazychengmm/cagent/internal/version"
)

// 退出码约定：0 成功，1 运行期错误，2 用法错误。
const (
	exitOK    = 0
	exitError = 1
	exitUsage = 2
)

const usageText = `cagent — 多角色运维 agent

用法:
  cagent agent  --config <path> [--log-level info]
  cagent server --config <path> [--log-level info]
  cagent ctl    <子命令> [参数...]
  cagent --version
`

// Run 执行一次命令分发，返回进程退出码。
func Run(argv []string, stdout, stderr io.Writer) int {
	if len(argv) == 0 {
		fmt.Fprint(stderr, usageText)
		return exitUsage
	}

	switch argv[0] {
	case "-h", "--help", "help":
		fmt.Fprint(stdout, usageText)
		return exitOK
	case "-v", "--version":
		fmt.Fprintln(stdout, version.String())
		return exitOK
	case "agent":
		return runAgent(argv[1:], stderr)
	case "server":
		return runServer(argv[1:], stderr)
	case "ctl":
		fmt.Fprintln(stderr, "cagent ctl 将在后续任务实现")
		return exitUsage
	default:
		fmt.Fprintf(stderr, "未知子命令 %q\n%s", argv[0], usageText)
		return exitUsage
	}
}

func runAgent(args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("agent", flag.ContinueOnError)
	fs.SetOutput(stderr)
	configPath := fs.String("config", config.DefaultAgentConfigPath(), "配置文件路径")
	logLevel := fs.String("log-level", "info", "日志级别 debug/info/warn/error")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}

	cfg, err := config.LoadAgentConfig(*configPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitError
	}

	logger, err := logging.New(*logLevel, stderr)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitError
	}

	// 业务控制循环在后续任务接入；当前仅确认配置与日志可用。
	// 不记录 enrollment_token（敏感信息，见 C-LOG-05）。
	logger.Info("agent 启动",
		"control_address", cfg.ControlAddress,
		"data_dir", cfg.DataDir,
		"labels", cfg.Labels)
	logger.Warn("agent 控制循环尚未实现，当前为骨架占位")
	return exitOK
}

func runServer(args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("server", flag.ContinueOnError)
	fs.SetOutput(stderr)
	configPath := fs.String("config", config.DefaultServerConfigPath(), "配置文件路径")
	logLevel := fs.String("log-level", "info", "日志级别 debug/info/warn/error")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}

	cfg, err := config.LoadServerConfig(*configPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitError
	}

	logger, err := logging.New(*logLevel, stderr)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitError
	}

	logger.Info("server 启动",
		"enrollment_listen", cfg.EnrollmentListen,
		"control_listen", cfg.ControlListen,
		"admin_listen", cfg.AdminListen,
		"data_dir", cfg.DataDir)
	logger.Warn("server gRPC 服务尚未实现，当前为骨架占位")
	return exitOK
}
