package ctl

import (
	"context"
	"flag"
	"fmt"
	"io"
	"time"

	pb "github.com/crazychengmm/cagent/proto/cagent/v1"
)

func runToken(configPath string, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "用法: token create|list|revoke")
		return 2
	}
	sub, rest := args[0], args[1:]
	ctx, cancel := context.WithTimeout(context.Background(), rpcTimeout)
	defer cancel()

	conn, err := dial(configPath, ctx, stderr)
	if err != nil {
		return 1
	}
	defer conn.Close()
	client := pb.NewManagementServiceClient(conn)

	switch sub {
	case "create":
		return tokenCreate(ctx, client, rest, stdout, stderr)
	case "list":
		return tokenList(ctx, client, stdout, stderr)
	case "revoke":
		return tokenRevoke(ctx, client, rest, stdout, stderr)
	default:
		fmt.Fprintf(stderr, "未知 token 子命令 %q\n", sub)
		return 2
	}
}

func tokenCreate(ctx context.Context, client pb.ManagementServiceClient,
	args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("token create", flag.ContinueOnError)
	fs.SetOutput(stderr)
	ttlHours := fs.Int64("ttl", 168, "有效期（小时），默认 168（7 天）")
	desc := fs.String("description", "", "描述")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	resp, err := client.CreateToken(ctx, &pb.CreateTokenRequest{
		TtlSeconds:  *ttlHours * 3600,
		Description: *desc,
	})
	if err != nil {
		fmt.Fprintf(stderr, "创建失败: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "token（仅此一次显示，请立即保存）:\n%s\n", resp.Plaintext)
	fmt.Fprintf(stdout, "id=%s 过期=%s\n",
		resp.Token.GetId(), resp.Token.GetExpiresAt().AsTime().Format(time.RFC3339))
	return 0
}

func tokenList(ctx context.Context, client pb.ManagementServiceClient,
	stdout, stderr io.Writer) int {
	resp, err := client.ListTokens(ctx, &pb.ListTokensRequest{})
	if err != nil {
		fmt.Fprintf(stderr, "查询失败: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "%-12s %-20s %-10s %-21s %s\n", "ID", "创建者", "状态", "到期", "描述")
	for _, t := range resp.Tokens {
		status := "active"
		if t.Revoked {
			status = "revoked"
		}
		fmt.Fprintf(stdout, "%-12s %-20s %-10s %-21s %s\n",
			t.Id, t.CreatedBy, status,
			t.ExpiresAt.AsTime().Format(time.RFC3339), t.Description)
	}
	fmt.Fprintf(stdout, "共 %d 个\n", len(resp.Tokens))
	return 0
}

func tokenRevoke(ctx context.Context, client pb.ManagementServiceClient,
	args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("token revoke", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "用法: token revoke <id>")
		return 2
	}
	id := fs.Arg(0)
	if _, err := client.RevokeToken(ctx, &pb.RevokeTokenRequest{Id: id}); err != nil {
		fmt.Fprintf(stderr, "吊销失败: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "已吊销 %s\n", id)
	return 0
}
