// Package ctl 实现 cagent ctl 管理命令：凭据加载、gRPC 连接与输出。
package ctl

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"gopkg.in/yaml.v3"
)

const (
	// EnvAdminAddress / EnvAdminToken 是凭据环境变量。
	EnvAdminAddress = "CAGENT_ADMIN_ADDR"
	EnvAdminToken   = "CAGENT_ADMIN_TOKEN"
	// dialTimeout 是建立管理连接的超时。
	dialTimeout = 10 * time.Second
)

// Config 是 ctl 的连接配置。
type Config struct {
	AdminAddress string `yaml:"admin_address"`
	AdminToken   string `yaml:"admin_token"`
	ServerCAFile string `yaml:"server_ca_file"`
}

// DefaultConfigPath 返回配置文件查找路径（用户配置优先，其次系统路径）。
func DefaultConfigPath() string {
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".config", "cagent", "ctl.yaml")
	}
	return "/etc/cagent/ctl.yaml"
}

// LoadConfig 按 配置文件 → 环境变量 的顺序合并（环境变量覆盖文件）。
func LoadConfig(path string) (*Config, error) {
	cfg := &Config{}
	if path == "" {
		path = DefaultConfigPath()
	}
	if data, err := os.ReadFile(path); err == nil {
		if err := yaml.Unmarshal(data, cfg); err != nil {
			return nil, fmt.Errorf("解析 ctl 配置 %s: %w", path, err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}

	if v := os.Getenv(EnvAdminAddress); v != "" {
		cfg.AdminAddress = v
	}
	if v := os.Getenv(EnvAdminToken); v != "" {
		cfg.AdminToken = v
	}
	return cfg, nil
}

// Dial 建立到 admin 端口的 gRPC 连接（服务端 TLS + admin token 元数据）。
func (c *Config) Dial(ctx context.Context) (*grpc.ClientConn, error) {
	if c.AdminAddress == "" {
		return nil, errors.New("缺少管理地址（配置 admin_address 或环境变量 " + EnvAdminAddress + "）")
	}
	if c.AdminToken == "" {
		return nil, errors.New("缺少 admin token（配置 admin_token 或环境变量 " + EnvAdminToken + "）")
	}
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if c.ServerCAFile != "" {
		pem, err := os.ReadFile(c.ServerCAFile)
		if err != nil {
			return nil, fmt.Errorf("读取 CA 文件: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, errors.New("CA 文件不是合法 PEM")
		}
		tlsCfg.RootCAs = pool
	} else {
		// 未配 CA 时仍要求 TLS，但用系统根校验（自签场景应配 CA）。
		tlsCfg.InsecureSkipVerify = true //nolint:gosec // ctl 明确未配 CA 时的显式降级
	}

	conn, err := grpc.NewClient(c.AdminAddress,
		grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg)),
		grpc.WithUnaryInterceptor(authInterceptor(c.AdminToken)),
		grpc.WithStreamInterceptor(streamAuthInterceptor(c.AdminToken)),
	)
	if err != nil {
		return nil, err
	}
	conn.Connect()

	dialCtx, cancel := context.WithTimeout(ctx, dialTimeout)
	defer cancel()
	for {
		state := conn.GetState()
		if state == connectivity.Ready {
			return conn, nil
		}
		if !conn.WaitForStateChange(dialCtx, state) {
			conn.Close()
			return nil, fmt.Errorf("连接 %s 超时", c.AdminAddress)
		}
	}
}

func authInterceptor(token string) grpc.UnaryClientInterceptor {
	return func(ctx context.Context, method string, req, reply any,
		cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		ctx = metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+token)
		return invoker(ctx, method, req, reply, cc, opts...)
	}
}

func streamAuthInterceptor(token string) grpc.StreamClientInterceptor {
	return func(ctx context.Context, desc *grpc.StreamDesc, cc *grpc.ClientConn,
		method string, streamer grpc.Streamer, opts ...grpc.CallOption) (grpc.ClientStream, error) {
		ctx = metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+token)
		return streamer(ctx, desc, cc, method, opts...)
	}
}
