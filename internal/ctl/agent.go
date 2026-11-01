package ctl

import (
	"context"
	"flag"
	"fmt"
	"io"
	"time"

	pb "github.com/crazychengmm/cagent/proto/cagent/v1"
)

func runAgentCmd(configPath string, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "用法: agent list|show <id>")
		return 2
	}
	sub, rest := args[0], args[1:]
	ctx, cancel := context.WithTimeout(context.Background(), rpcTimeout)
	defer cancel()
	conn, err := dial(configPath, ctx, stderr)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer conn.Close()
	client := pb.NewManagementServiceClient(conn)

	switch sub {
	case "list":
		return agentList(ctx, client, stdout, stderr)
	case "show":
		return agentShow(ctx, client, rest, stdout, stderr)
	default:
		fmt.Fprintf(stderr, "未知 agent 子命令 %q\n", sub)
		return 2
	}
}

func agentList(ctx context.Context, client pb.ManagementServiceClient,
	stdout, stderr io.Writer) int {
	resp, err := client.ListAgents(ctx, &pb.ListAgentsRequest{})
	if err != nil {
		fmt.Fprintf(stderr, "查询失败: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "%-38s %-16s %-9s %-9s %s\n", "ID", "主机名", "状态", "版本", "标签")
	for _, a := range resp.Agents {
		fmt.Fprintf(stdout, "%-38s %-16s %-9s %-9s %s\n",
			a.Id, a.Hostname, a.Status, a.Version, labelsString(a.Labels))
	}
	fmt.Fprintf(stdout, "共 %d 台\n", len(resp.Agents))
	return 0
}

func agentShow(ctx context.Context, client pb.ManagementServiceClient,
	args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("agent show", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "用法: agent show <id>")
		return 2
	}
	resp, err := client.ShowAgent(ctx, &pb.ShowAgentRequest{Id: fs.Arg(0)})
	if err != nil {
		fmt.Fprintf(stderr, "查询失败: %v\n", err)
		return 1
	}
	a := resp.Agent
	fmt.Fprintf(stdout, "ID:       %s\n", a.Id)
	fmt.Fprintf(stdout, "主机名:   %s\n", a.Hostname)
	fmt.Fprintf(stdout, "状态:     %s\n", a.Status)
	fmt.Fprintf(stdout, "版本:     %s\n", a.Version)
	fmt.Fprintf(stdout, "标签:     %s\n", labelsString(a.Labels))
	if a.LastHeartbeatAt != nil {
		fmt.Fprintf(stdout, "最后心跳: %s\n", a.LastHeartbeatAt.AsTime().Format(time.RFC3339))
	}
	if a.CertNotAfter != nil {
		fmt.Fprintf(stdout, "证书到期: %s\n", a.CertNotAfter.AsTime().Format(time.RFC3339))
	}
	return 0
}

func labelsString(labels map[string]string) string {
	if len(labels) == 0 {
		return "-"
	}
	out := ""
	for k, v := range labels {
		if out != "" {
			out += ","
		}
		out += k + "=" + v
	}
	return out
}
