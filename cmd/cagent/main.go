// Command cagent 是多角色运维 agent 系统的单一二进制，
// 含 agent（被控端）、server（控制端）、ctl（管理 CLI）三个子命令。
package main

import (
	"os"

	"github.com/crazychengmm/cagent/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:], os.Stdout, os.Stderr))
}
