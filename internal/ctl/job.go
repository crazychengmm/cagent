package ctl

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	pb "github.com/crazychengmm/cagent/proto/cagent/v1"
)

func runJobCmd(configPath string, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "用法: job run|list|show|cancel")
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
	case "run":
		return jobRun(ctx, client, rest, stdout, stderr)
	case "list":
		return jobList(ctx, client, rest, stdout, stderr)
	case "show":
		return jobShow(ctx, client, rest, stdout, stderr)
	case "cancel":
		return jobCancel(ctx, client, rest, stdout, stderr)
	default:
		fmt.Fprintf(stderr, "未知 job 子命令 %q\n", sub)
		return 2
	}
}

func parseSelectors(values []string) map[string]string {
	out := map[string]string{}
	for _, v := range values {
		k, val, ok := strings.Cut(v, "=")
		if ok {
			out[k] = val
		}
	}
	return out
}

func jobRun(ctx context.Context, client pb.ManagementServiceClient,
	args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("job run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var labels multiFlag
	fs.Var(&labels, "selector", "标签 k=v，可多次（AND）")
	timeout := fs.Int64("timeout", 300, "超时秒数，默认 300，上限 3600")
	workdir := fs.String("working-dir", "", "工作目录")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "用法: job run [--selector k=v]... [--timeout N] [--working-dir DIR] <command>")
		return 2
	}
	resp, err := client.RunJob(ctx, &pb.RunJobRequest{
		Selector:       parseSelectors(labels),
		Command:        fs.Arg(0),
		WorkingDir:     *workdir,
		TimeoutSeconds: *timeout,
	})
	if err != nil {
		fmt.Fprintf(stderr, "下发失败: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "已创建 %d 个任务:\n", resp.Matched)
	for _, id := range resp.JobIds {
		fmt.Fprintln(stdout, id)
	}
	return 0
}

type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

func jobList(ctx context.Context, client pb.ManagementServiceClient,
	args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("job list", flag.ContinueOnError)
	fs.SetOutput(stderr)
	agentID := fs.String("agent", "", "按 agent 过滤")
	status := fs.String("status", "", "按状态过滤")
	limit := fs.Int("limit", 100, "返回上限")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	resp, err := client.ListJobs(ctx, &pb.ListJobsRequest{
		AgentId: *agentID, Status: *status, Limit: int32(*limit),
	})
	if err != nil {
		fmt.Fprintf(stderr, "查询失败: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "%-40s %-38s %-10s %-9s %s\n", "JOB", "AGENT", "类型", "状态", "命令")
	for _, j := range resp.Jobs {
		fmt.Fprintf(stdout, "%-40s %-38s %-10s %-9s %s\n",
			j.JobId, j.AgentId, j.Kind, j.Status, truncate(j.Command, 40))
	}
	fmt.Fprintf(stdout, "共 %d 个\n", len(resp.Jobs))
	return 0
}

func jobShow(ctx context.Context, client pb.ManagementServiceClient,
	args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("job show", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "用法: job show <id>")
		return 2
	}
	resp, err := client.ShowJob(ctx, &pb.ShowJobRequest{Id: fs.Arg(0)})
	if err != nil {
		fmt.Fprintf(stderr, "查询失败: %v\n", err)
		return 1
	}
	j := resp.Job
	fmt.Fprintf(stdout, "ID:        %s\n", j.JobId)
	fmt.Fprintf(stdout, "AGENT:     %s\n", j.AgentId)
	fmt.Fprintf(stdout, "类型:      %s\n", j.Kind)
	fmt.Fprintf(stdout, "状态:      %s\n", j.Status)
	fmt.Fprintf(stdout, "命令:      %s\n", j.Command)
	fmt.Fprintf(stdout, "操作者:    %s\n", j.RequestedBy)
	fmt.Fprintf(stdout, "创建时间:  %s\n", j.CreatedAt.AsTime().Format(time.RFC3339))
	if j.ExitCode != nil {
		fmt.Fprintf(stdout, "退出码:    %d\n", *j.ExitCode)
	}
	if j.Truncated {
		fmt.Fprintln(stdout, "输出:      （超过 1MB 已截断）")
	}
	if j.Message != "" {
		fmt.Fprintf(stdout, "消息:      %s\n", j.Message)
	}
	if len(resp.Output) > 0 {
		fmt.Fprintln(stdout, "---- 输出 ----")
		for _, c := range resp.Output {
			fmt.Fprint(stdout, string(c.Data))
			if len(c.Data) > 0 && c.Data[len(c.Data)-1] != '\n' {
				fmt.Fprintln(stdout)
			}
		}
	}
	return 0
}

func jobCancel(ctx context.Context, client pb.ManagementServiceClient,
	args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("job cancel", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "用法: job cancel <id>")
		return 2
	}
	id := fs.Arg(0)
	if _, err := client.CancelJob(ctx, &pb.CancelJobRequest{Id: id}); err != nil {
		fmt.Fprintf(stderr, "取消失败: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "已下发取消 %s\n", id)
	return 0
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
