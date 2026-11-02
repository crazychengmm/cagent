package ctl

import (
	"fmt"
	"io"
)

// Run 执行 ctl 子命令，返回进程退出码。
func Run(args []string, stdout, stderr io.Writer) int {
	path, rest := splitConfigFlag(args)
	if len(rest) == 0 {
		fmt.Fprint(stderr, ctlUsage)
		return 2
	}
	cmd, subArgs := rest[0], rest[1:]
	switch cmd {
	case "token":
		return runToken(path, subArgs, stdout, stderr)
	case "agent":
		return runAgentCmd(path, subArgs, stdout, stderr)
	case "job":
		return runJobCmd(path, subArgs, stdout, stderr)
	case "release":
		return runReleaseCmd(path, subArgs, stdout, stderr)
	case "upgrade":
		return runUpgradeCmd(path, subArgs, stdout, stderr)
	case "-h", "--help", "help":
		fmt.Fprint(stdout, ctlUsage)
		return 0
	default:
		fmt.Fprintf(stderr, "未知子命令 %q\n%s", cmd, ctlUsage)
		return 2
	}
}

// splitConfigFlag 从参数中提取 --config=xxx / --config xxx，返回路径与剩余参数。
func splitConfigFlag(args []string) (string, []string) {
	var path string
	rest := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--config" && i+1 < len(args):
			path = args[i+1]
			i++
		case len(a) > 9 && a[:9] == "--config=":
			path = a[9:]
		default:
			rest = append(rest, a)
		}
	}
	return path, rest
}

const ctlUsage = `cagent ctl — 管理客户端

用法: cagent ctl <command> [flags]

全局: --config <path>   ctl 配置（默认 ~/.config/cagent/ctl.yaml）
      环境变量 CAGENT_ADMIN_ADDR / CAGENT_ADMIN_TOKEN

命令:
  token create|list|revoke   enrollment token 管理
  agent list|show            agent 清单
  job run|list|show|cancel   任务管理
  release keygen|package|sign|publish|list  签名密钥与发布物管理
  upgrade                    按标签发起升级
`
