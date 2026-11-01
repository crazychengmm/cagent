// Package cli 实现 cagent 二进制的子命令分发（agent/server/ctl）。
package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"google.golang.org/grpc"

	"github.com/crazychengmm/cagent/internal/config"
	"github.com/crazychengmm/cagent/internal/logging"
	"github.com/crazychengmm/cagent/internal/server/admin"
	"github.com/crazychengmm/cagent/internal/server/ca"
	"github.com/crazychengmm/cagent/internal/server/controlservice"
	"github.com/crazychengmm/cagent/internal/server/enrollment"
	"github.com/crazychengmm/cagent/internal/server/enrollservice"
	"github.com/crazychengmm/cagent/internal/server/serve"
	"github.com/crazychengmm/cagent/internal/server/store"
	"github.com/crazychengmm/cagent/internal/version"
	pb "github.com/crazychengmm/cagent/proto/cagent/v1"
)

// 退出码约定：0 成功，1 运行期错误，2 用法错误。
const (
	exitOK    = 0
	exitError = 1
	exitUsage = 2
)

const (
	caDirName      = "ca"
	serverDBName   = "cagent-server.db"
	adminTokenName = "admin_token"
)

const usageText = `cagent — 多角色运维 agent

用法:
  cagent agent  --config <path> [--log-level info]
  cagent server --config <path> [--log-level info]
  cagent ctl    <子命令> [参数...]
  cagent --version
`

// Run 执行一次命令分发，返回进程退出码。
func Run(argv []string, stdout, stderr io.Writer) int {
	if len(argv) == 0 {
		fmt.Fprint(stderr, usageText)
		return exitUsage
	}

	switch argv[0] {
	case "-h", "--help", "help":
		fmt.Fprint(stdout, usageText)
		return exitOK
	case "-v", "--version":
		fmt.Fprintln(stdout, version.String())
		return exitOK
	case "agent":
		return runAgent(argv[1:], stderr)
	case "server":
		return runServer(argv[1:], stderr)
	case "ctl":
		fmt.Fprintln(stderr, "cagent ctl 将在后续任务实现")
		return exitUsage
	default:
		fmt.Fprintf(stderr, "未知子命令 %q\n%s", argv[0], usageText)
		return exitUsage
	}
}

func runAgent(args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("agent", flag.ContinueOnError)
	fs.SetOutput(stderr)
	configPath := fs.String("config", config.DefaultAgentConfigPath(), "配置文件路径")
	logLevel := fs.String("log-level", "info", "日志级别 debug/info/warn/error")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}

	cfg, err := config.LoadAgentConfig(*configPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitError
	}

	logger, err := logging.New(*logLevel, stderr)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitError
	}

	// 业务控制循环在后续任务接入；当前仅确认配置与日志可用。
	// 不记录 enrollment_token（敏感信息，见 C-LOG-05）。
	logger.Info("agent 启动",
		"control_address", cfg.ControlAddress,
		"data_dir", cfg.DataDir,
		"labels", cfg.Labels)
	logger.Warn("agent 控制循环尚未实现，当前为骨架占位")
	return exitOK
}

func runServer(args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("server", flag.ContinueOnError)
	fs.SetOutput(stderr)
	configPath := fs.String("config", config.DefaultServerConfigPath(), "配置文件路径")
	logLevel := fs.String("log-level", "info", "日志级别 debug/info/warn/error")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}

	cfg, err := config.LoadServerConfig(*configPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitError
	}
	logger, err := logging.New(*logLevel, stderr)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitError
	}

	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		logger.Error("创建数据目录失败", "dir", cfg.DataDir, "error", err.Error())
		return exitError
	}

	db, err := store.Open(context.Background(), filepath.Join(cfg.DataDir, serverDBName))
	if err != nil {
		logger.Error("打开数据库失败", "error", err.Error())
		return exitError
	}
	defer db.Close()

	// admin token：首启生成并提示一次位置，不打印内容（C-LOG-05）。
	tokenPath := filepath.Join(cfg.DataDir, adminTokenName)
	if _, err := os.Stat(tokenPath); os.IsNotExist(err) {
		if _, err := admin.EnsureAdminToken(cfg.DataDir); err != nil {
			logger.Error("生成 admin token 失败", "error", err.Error())
			return exitError
		}
		logger.Info("已生成 admin token，请妥善读取后配置 ctl", "path", tokenPath)
	}
	adminToken, err := admin.EnsureAdminToken(cfg.DataDir)
	if err != nil {
		logger.Error("读取 admin token 失败", "error", err.Error())
		return exitError
	}

	caDir := filepath.Join(cfg.DataDir, caDirName)
	rootCA, err := ca.LoadOrCreate(caDir)
	if err != nil {
		logger.Error("加载内置 CA 失败", "error", err.Error())
		return exitError
	}
	serverCert, err := rootCA.EnsureServerCertificate(caDir, cfg.ServerCertHosts, config.ServerCertValidity)
	if err != nil {
		logger.Error("准备 server 证书失败", "error", err.Error())
		return exitError
	}

	tokenService := enrollment.NewTokenService(db, cfg.EnrollmentTokenTTL.Std(), nil)
	enrollService := enrollservice.New(db, tokenService, rootCA,
		cfg.AgentCertValidity.Std(), nil, logger)
	controlService := controlservice.New(db, rootCA, cfg.AgentCertValidity.Std(), nil, logger)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	logger.Info("server 启动",
		"enrollment_listen", cfg.EnrollmentListen,
		"control_listen", cfg.ControlListen,
		"admin_listen", cfg.AdminListen,
		"data_dir", cfg.DataDir)
	err = serve.Run(ctx, serve.Deps{
		EnrollmentListen: cfg.EnrollmentListen,
		ControlListen:    cfg.ControlListen,
		AdminListen:      cfg.AdminListen,
		EnrollService:    enrollService,
		ServerCert:       *serverCert,
		CAPEM:            rootCA.CACertPEM(),
		AdminToken:       adminToken,
		Store:            db,
		Logger:           logger,
		RegisterControl: func(s *grpc.Server) {
			pb.RegisterControlServiceServer(s, controlService)
		},
	})
	if err != nil {
		logger.Error("server 运行失败", "error", err.Error())
		return exitError
	}
	return exitOK
}
