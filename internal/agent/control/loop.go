package control

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"math/rand"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials"

	pb "github.com/crazychengmm/cagent/proto/cagent/v1"
)

const (
	initialBackoff     = time.Second
	maxBackoff         = 30 * time.Second
	backoffResetAfter  = 30 * time.Second
	dialTimeout        = 10 * time.Second
	defaultHeartbeat   = 15 * time.Second
	defaultRenewBefore = 30 * 24 * time.Hour
)

// errRenewed 表示证书刚续签，应立即用新证书重连（不计失败退避）。
var errRenewed = errors.New("证书已续签，主动重连")

// Loop 是 agent 控制循环：维持一条 mTLS Control 双向流，断线自动重连。
type Loop struct {
	cfg Config
}

// New 校验并构造循环；未显式设置的间隔取默认值。
func New(cfg Config) (*Loop, error) {
	if cfg.Identity == nil {
		return nil, errors.New("identity 不能为空")
	}
	if cfg.ServerAddr == "" {
		return nil, errors.New("server 地址不能为空")
	}
	if cfg.HeartbeatInterval <= 0 {
		cfg.HeartbeatInterval = defaultHeartbeat
	}
	if cfg.RenewBefore <= 0 {
		cfg.RenewBefore = defaultRenewBefore
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Loop{cfg: cfg}, nil
}

// Run 阻塞运行直到 ctx 取消；取消时正常返回 nil。
func (l *Loop) Run(ctx context.Context) error {
	attempt := 0
	for {
		if ctx.Err() != nil {
			return nil
		}
		connectedAt, err := l.runOnce(ctx)
		if ctx.Err() != nil {
			return nil
		}
		l.setState(false)

		switch {
		case errors.Is(err, errRenewed):
			attempt = 0
		case time.Since(connectedAt) > backoffResetAfter:
			attempt = 0
		}

		wait := backoff(attempt)
		attempt++
		l.cfg.Logger.Warn("控制连接断开，退避后重连",
			"wait", wait.String(), "attempt", attempt, "error", errText(err))
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return nil
		}
	}
}

func (l *Loop) setState(connected bool) {
	if l.cfg.OnStateChange != nil {
		l.cfg.OnStateChange(connected)
	}
}

// runOnce 建立一条连接并在其上运行会话，返回连接建立时刻与会话结束原因。
func (l *Loop) runOnce(ctx context.Context) (time.Time, error) {
	tlsCfg, err := clientTLS(l.cfg.Identity)
	if err != nil {
		return time.Time{}, err
	}
	conn, err := grpc.NewClient(l.cfg.ServerAddr,
		grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg)))
	if err != nil {
		return time.Time{}, err
	}
	defer conn.Close()

	if err := waitReady(ctx, conn, dialTimeout); err != nil {
		return time.Time{}, err
	}

	client := pb.NewControlServiceClient(conn)
	stream, err := client.Control(ctx)
	if err != nil {
		return time.Time{}, err
	}
	connectedAt := time.Now()
	l.setState(true)
	l.cfg.Logger.Info("已连接控制通道", "server", l.cfg.ServerAddr)

	session := &session{cfg: l.cfg, client: client, stream: stream}
	if l.cfg.OnConnected != nil {
		l.cfg.OnConnected(ctx, session)
	}
	return connectedAt, session.run(ctx)
}

// WaitReady 等待连接就绪（导出供独立短连接使用）。
func WaitReady(ctx context.Context, conn *grpc.ClientConn, timeout time.Duration) error {
	return waitReady(ctx, conn, timeout)
}

func waitReady(ctx context.Context, conn *grpc.ClientConn, timeout time.Duration) error {
	readyCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	conn.Connect()
	for {
		state := conn.GetState()
		if state == connectivity.Ready {
			return nil
		}
		if !conn.WaitForStateChange(readyCtx, state) {
			if readyCtx.Err() != nil {
				return readyCtx.Err()
			}
			return errors.New("等待连接就绪中断")
		}
	}
}

// backoff 返回 1s·2^n（封顶 30s）并叠加 0..20% 抖动。
func backoff(attempt int) time.Duration {
	d := initialBackoff << attempt
	if d <= 0 || d > maxBackoff {
		d = maxBackoff
	}
	jitter := time.Duration(rand.Int63n(int64(d) / 5))
	return d + jitter
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
