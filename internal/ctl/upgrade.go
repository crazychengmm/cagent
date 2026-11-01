package ctl

import (
	"context"
	"flag"
	"fmt"
	"io"

	pb "github.com/crazychengmm/cagent/proto/cagent/v1"
)

func runUpgradeCmd(configPath string, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("upgrade", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var labels multiFlag
	fs.Var(&labels, "selector", "标签 k=v，可多次（AND）")
	version := fs.String("release", "", "目标版本（必填）")
	allowDowngrade := fs.Bool("allow-downgrade", false, "显式允许降级")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *version == "" {
		fmt.Fprintln(stderr, "用法: upgrade --release <version> [--selector k=v]... [--allow-downgrade]")
		return 2
	}

	ctx, cancel := context.WithTimeout(context.Background(), rpcTimeout)
	defer cancel()
	conn, err := dial(configPath, ctx, stderr)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer conn.Close()

	resp, err := pb.NewManagementServiceClient(conn).Upgrade(ctx, &pb.UpgradeRequest{
		Selector:       parseSelectors(labels),
		Version:        *version,
		AllowDowngrade: *allowDowngrade,
	})
	if err != nil {
		fmt.Fprintf(stderr, "升级下发失败: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "已向 %d 台匹配 agent 下发升级到 %s（降级=%v）:\n",
		resp.Matched, *version, *allowDowngrade)
	for _, id := range resp.JobIds {
		fmt.Fprintln(stdout, id)
	}
	return 0
}
