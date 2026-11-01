// Command cagent 是多角色运维 agent 系统的单一二进制，
// 子命令 agent/server/ctl 的分发在后续任务接入（当前仅支持 --version）。
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/crazychengmm/cagent/internal/version"
)

func main() {
	showVersion := flag.Bool("version", false, "打印版本信息并退出")
	flag.Parse()

	if *showVersion {
		fmt.Println(version.String())
		return
	}

	fmt.Fprintln(os.Stderr, "用法: cagent <agent|server|ctl> [flags]（子命令将在后续任务中实现）")
	os.Exit(2)
}
