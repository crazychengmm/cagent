// Package cli 实现 cagent 二进制的子命令分发（agent/server/ctl）。
package cli

import (
	"context"
	"crypto/ed25519"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"google.golang.org/grpc"

	agentaudit "github.com/crazychengmm/cagent/internal/agent/audit"
	"github.com/crazychengmm/cagent/internal/agent/control"
	"github.com/crazychengmm/cagent/internal/agent/executor"
	"github.com/crazychengmm/cagent/internal/agent/identity"
	"github.com/crazychengmm/cagent/internal/agent/jobworker"
	"github.com/crazychengmm/cagent/internal/agent/register"
	"github.com/crazychengmm/cagent/internal/agent/trust"
	agentupgrade "github.com/crazychengmm/cagent/internal/agent/upgrade"
	"github.com/crazychengmm/cagent/internal/config"
	ctlcmd "github.com/crazychengmm/cagent/internal/ctl"
	"github.com/crazychengmm/cagent/internal/logging"
	"github.com/crazychengmm/cagent/internal/release"
	"github.com/crazychengmm/cagent/internal/server/admin"
	"github.com/crazychengmm/cagent/internal/server/ca"
	"github.com/crazychengmm/cagent/internal/server/controlservice"
	"github.com/crazychengmm/cagent/internal/server/dispatch"
	"github.com/crazychengmm/cagent/internal/server/enrollment"
	"github.com/crazychengmm/cagent/internal/server/enrollservice"
	"github.com/crazychengmm/cagent/internal/server/management"
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
		return ctlcmd.Run(argv[1:], stdout, stderr)
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
	guardMode := fs.String("guard-mode", "", "升级 guard 状态文件（内部使用）")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if *guardMode != "" {
		return runGuardMode(*guardMode, *configPath, *logLevel, stderr)
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

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	ident, code := loadOrEnroll(ctx, cfg, logger)
	if code != exitOK {
		return code
	}

	auditor, err := agentaudit.OpenFileLogger(cfg.DataDir)
	if err != nil {
		logger.Error("打开本地审计文件失败", "error", err.Error())
		return exitError
	}
	defer auditor.Close()

	manager := jobworker.NewManager(cfg.JobConcurrency,
		executor.NewLocalExecutor(), logger, auditor)
	manager.Start(ctx)

	var signingPub ed25519.PublicKey
	if cfg.SigningPubkeyFile != "" {
		pubPEM, err := os.ReadFile(cfg.SigningPubkeyFile)
		if err != nil {
			logger.Error("读取升级签名公钥失败", "error", err.Error())
			return exitError
		}
		signingPub, err = release.ParsePublicKeyPEM(pubPEM)
		if err != nil {
			logger.Error("解析升级签名公钥失败", "error", err.Error())
			return exitError
		}
	}
	upgradeHandler := agentupgrade.NewHandler(agentupgrade.HandlerConfig{
		Identity:       ident,
		ServerAddr:     cfg.ControlAddress,
		DataDir:        cfg.DataDir,
		ConfigPath:     *configPath,
		CurrentVersion: version.Version,
		SigningPub:     signingPub,
		Tracker:        manager,
		Logger:         logger,
		RestartCommand: cfg.RestartCommand,
	})

	logger.Info("agent 启动",
		"agent_id", ident.AgentID,
		"control_address", cfg.ControlAddress,
		"data_dir", cfg.DataDir,
		"labels", cfg.Labels,
		"job_concurrency", cfg.JobConcurrency)

	loop, err := control.New(control.Config{
		Identity:          ident,
		ServerAddr:        cfg.ControlAddress,
		DataDir:           cfg.DataDir,
		HeartbeatInterval: cfg.HeartbeatInterval.Std(),
		Labels:            cfg.Labels,
		Version:           version.Version,
		JobHandler:        manager,
		UpgradeHandler:    upgradeHandler,
		OnStateChange:     markHealthyOnConnect(cfg.DataDir, version.Version),
		OnConnected: func(ctx context.Context, sender control.Sender) {
			// 升级结果由重启后的 agent 经稳定主连接上报（等待 guard 落盘）。
			go func() {
				if err := agentupgrade.ReportPendingResults(ctx, cfg.DataDir, sender); err != nil {
					logger.Warn("升级结果等待/上报中断，将由新连接重试", "error", err.Error())
				}
			}()
		},
		Logger: logger,
	})
	if err != nil {
		logger.Error("构造控制循环失败", "error", err.Error())
		return exitError
	}
	if err := loop.Run(ctx); err != nil {
		logger.Error("控制循环异常退出", "error", err.Error())
		return exitError
	}
	logger.Info("agent 已停止")
	return exitOK
}

// loadOrEnroll 加载既有身份；无身份时用引导令牌与信任锚点完成首次注册并落盘。
func loadOrEnroll(ctx context.Context, cfg *config.AgentConfig,
	logger *slog.Logger) (*identity.Identity, int) {
	if identity.Exists(cfg.DataDir) {
		ident, err := identity.Load(cfg.DataDir)
		if err != nil {
			logger.Error("加载本地身份失败", "error", err.Error())
			return nil, exitError
		}
		return ident, exitOK
	}

	logger.Info("未发现本地身份，开始首次注册")
	if cfg.EnrollmentAddress == "" {
		logger.Error("首次注册需要 enrollment_address")
		return nil, exitError
	}
	if cfg.EnrollmentToken == "" {
		logger.Error("首次注册需要 enrollment_token")
		return nil, exitError
	}
	tlsCfg, err := trust.EnrollmentTLS(cfg.ServerCAFile, cfg.ServerCAFingerprint)
	if err != nil {
		logger.Error("加载 server 信任锚点失败（必须提供 CA 文件或指纹）", "error", err.Error())
		return nil, exitError
	}
	hostname, err := os.Hostname()
	if err != nil {
		logger.Error("获取主机名失败", "error", err.Error())
		return nil, exitError
	}
	key, err := identity.GenerateKey()
	if err != nil {
		logger.Error("生成私钥失败", "error", err.Error())
		return nil, exitError
	}

	// 禁止记录 token 内容（C-LOG-05）。
	result, err := register.Enroll(ctx, cfg.EnrollmentAddress, tlsCfg,
		cfg.EnrollmentToken, hostname, cfg.Labels, key)
	if err != nil {
		logger.Error("注册失败", "server", cfg.EnrollmentAddress, "error", err.Error())
		return nil, exitError
	}
	if err := identity.Save(cfg.DataDir, result.AgentID, key, result.CertPEM, result.CAPEM); err != nil {
		logger.Error("持久化身份失败", "error", err.Error())
		return nil, exitError
	}
	logger.Info("首次注册成功", "agent_id", result.AgentID)

	ident, err := identity.Load(cfg.DataDir)
	if err != nil {
		logger.Error("回读身份失败", "error", err.Error())
		return nil, exitError
	}
	return ident, exitOK
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

	caDir := filepath.Join(cfg.DataDir, ca.CADirName)
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
	controlService := controlservice.New(db, rootCA, cfg.AgentCertValidity.Std(),
		filepath.Join(cfg.DataDir, management.ReleaseDirName), nil, logger)

	var signingPub ed25519.PublicKey
	if cfg.ReleaseSigningPubkeyFile != "" {
		pubPEM, err := os.ReadFile(cfg.ReleaseSigningPubkeyFile)
		if err != nil {
			logger.Error("读取发布签名公钥失败", "error", err.Error())
			return exitError
		}
		signingPub, err = release.ParsePublicKeyPEM(pubPEM)
		if err != nil {
			logger.Error("解析发布签名公钥失败", "error", err.Error())
			return exitError
		}
	}
	dispatchService := dispatch.New(db, controlService, nil, logger)
	managementService, err := management.New(db, cfg.DataDir, signingPub,
		controlService, tokenService, dispatchService, nil, logger)
	if err != nil {
		logger.Error("初始化管理服务失败", "error", err.Error())
		return exitError
	}

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
		HeartbeatTimeout: cfg.HeartbeatTimeout.Std(),
		QueueTTL:         cfg.QueueTTL.Std(),
		SweepInterval:    10 * time.Second,
		RegisterControl: func(s *grpc.Server) {
			pb.RegisterControlServiceServer(s, controlService)
		},
		RegisterAdmin: func(s *grpc.Server) {
			pb.RegisterManagementServiceServer(s, managementService)
		},
	})
	if err != nil {
		logger.Error("server 运行失败", "error", err.Error())
		return exitError
	}
	return exitOK
}
