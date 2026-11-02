package ctl

import (
	"context"
	"fmt"
	"io"
	"time"

	"google.golang.org/grpc"
)

const rpcTimeout = 30 * time.Second

// dial 加载凭据并建立管理连接。
func dial(configPath string, ctx context.Context, stderr io.Writer) (*grpc.ClientConn, error) {
	cfg, err := LoadConfig(configPath)
	if err != nil {
		return nil, err
	}
	conn, err := cfg.Dial(ctx)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return nil, err
	}
	return conn, nil
}
