package upgrade

import (
	"context"
	"crypto/ed25519"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	"github.com/crazychengmm/cagent/internal/agent/control"
	"github.com/crazychengmm/cagent/internal/agent/identity"
	pb "github.com/crazychengmm/cagent/proto/cagent/v1"
)

// DefaultSystemdUnit 是 agent 服务默认单元名。
const DefaultSystemdUnit = "cagent.service"

// dialWait 是下载升级包时建立控制连接的超时。
const dialWait = 10 * time.Second

// Handler 实现 control.UpgradeHandler：下载验签后执行升级编排。
type Handler struct {
	ident          *identity.Identity
	serverAddr     string
	dataDir        string
	configPath     string
	currentVersion string
	signingPub     ed25519.PublicKey
	tracker        ActiveTracker
	logger         *slog.Logger
	unit           string
	restartCommand string

	// inProcess 为 true 时 Prepare 后在同进程执行健康门控（测试用）；
	// 生产为 false：Prepare 触发 systemctl 重启，健康门控由 detach 的 guard 完成。
	inProcess bool

	// 单飞锁：防止在线下发与重连补投并发触发两个升级操作同一二进制。
	inFlightMu sync.Mutex
	inFlight   bool
}

// HandlerConfig 是升级处理器配置。
type HandlerConfig struct {
	Identity       *identity.Identity
	ServerAddr     string
	DataDir        string
	ConfigPath     string
	CurrentVersion string
	SigningPub     ed25519.PublicKey
	Tracker        ActiveTracker
	Logger         *slog.Logger
	Unit           string
	// RestartCommand 非空时替代 systemctl（测试/非 systemd 场景）。
	RestartCommand string
}

// NewHandler 构造生产模式处理器。
func NewHandler(cfg HandlerConfig) *Handler {
	unit := cfg.Unit
	if unit == "" {
		unit = DefaultSystemdUnit
	}
	return &Handler{
		ident:          cfg.Identity,
		serverAddr:     cfg.ServerAddr,
		dataDir:        cfg.DataDir,
		configPath:     cfg.ConfigPath,
		currentVersion: cfg.CurrentVersion,
		signingPub:     cfg.SigningPub,
		tracker:        cfg.Tracker,
		logger:         cfg.Logger,
		unit:           unit,
		restartCommand: cfg.RestartCommand,
	}
}

// OnUpgrade 实现 control.UpgradeHandler。
//
// 升级流程（下载/验签/grace/替换/重启）刻意不使用传入的 stream ctx：
// 升级期间控制流可能因网络抖动或 agent 即将自重启而断开，但升级必须走完
// （成功替换重启，或失败上报并保持旧版）。改用带总超时的独立 context。
func (h *Handler) OnUpgrade(_ context.Context, sender control.Sender, cmd *pb.UpgradeCommand) {
	// 单飞：同一时刻只允许一个升级流程（在线下发与重连补投可能并发到达）。
	h.inFlightMu.Lock()
	if h.inFlight {
		h.inFlightMu.Unlock()
		h.logger.Warn("已有升级在进行，忽略重复升级指令", "job_id", cmd.GetJobId())
		return
	}
	h.inFlight = true
	h.inFlightMu.Unlock()
	defer func() {
		h.inFlightMu.Lock()
		h.inFlight = false
		h.inFlightMu.Unlock()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), DefaultTotalTimeout)
	defer cancel()

	reporter := &StreamReporter{Sender: sender, JobID: cmd.GetJobId()}

	// 未安装签名公钥时拒绝一切升级（而不是下载后才以验签失败告终）。
	if len(h.signingPub) != ed25519.PublicKeySize {
		failUpgrade(reporter, "agent 未配置升级签名公钥（signing_pubkey_file），拒绝升级")
		return
	}

	tlsCfg, err := control.ClientTLSConfig(h.ident)
	if err != nil {
		failUpgrade(reporter, "构造 mTLS 配置失败: "+err.Error())
		return
	}
	conn, err := grpc.NewClient(h.serverAddr,
		grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg)))
	if err != nil {
		failUpgrade(reporter, "连接控制服务失败: "+err.Error())
		return
	}
	defer conn.Close()

	dialCtx, cancel := context.WithTimeout(ctx, dialWait)
	defer cancel()
	if err := control.WaitReady(dialCtx, conn, dialWait); err != nil {
		failUpgrade(reporter, "控制连接不可用: "+err.Error())
		return
	}
	client := pb.NewControlServiceClient(conn)
	pkg, err := DownloadVerify(ctx, client, Expected{
		Version:   cmd.GetVersion(),
		SHA256Hex: cmd.GetSha256Hex(),
		Size:      cmd.GetPackageSize(),
		Signature: cmd.GetSignature(),
	}, h.signingPub)
	if err != nil {
		failUpgrade(reporter, "下载验签失败: "+err.Error())
		return
	}

	binaryPath, err := CurrentBinaryPath()
	if err != nil {
		failUpgrade(reporter, err.Error())
		return
	}
	state := &UpgradeState{
		JobID:          cmd.GetJobId(),
		FromVersion:    h.currentVersion,
		ToVersion:      cmd.GetVersion(),
		BinaryPath:     binaryPath,
		BackupPath:     filepath.Join(h.dataDir, releaseDirName, backupFileName),
		StagedPath:     filepath.Join(h.dataDir, releaseDirName, stagedFileName),
		Unit:           h.unit,
		RestartCommand: h.restartCommand,
	}
	if err := os.MkdirAll(filepath.Dir(state.BackupPath), 0o700); err != nil {
		failUpgrade(reporter, "准备 releases 目录失败: "+err.Error())
		return
	}
	if err := SaveState(StatePath(h.dataDir), state); err != nil {
		failUpgrade(reporter, "写升级状态失败: "+err.Error())
		return
	}

	installer := h.newInstaller(state)
	deps := Deps{
		Tracker:       h.tracker,
		Installer:     installer,
		Reporter:      reporter,
		GraceTimeout:  DefaultGraceTimeout,
		HealthTimeout: DefaultHealthTimeout,
	}
	prepared := Prepare(ctx, cmd.GetJobId(), h.currentVersion, cmd, pkg, deps)
	if prepared.State != StateRestarted {
		// 早期失败（版本门控/grace/验签/替换）不会重启，清理本进程写的现场，
		// 避免残留 state 干扰后续升级（guard 只在真正重启后才接管）。
		h.logger.Error("升级在重启前失败", "reason", prepared.Reason)
		_ = ClearState(StatePath(h.dataDir))
		return
	}
	h.logger.Info("升级重启已触发", "version", cmd.GetVersion())

	if h.inProcess {
		// 测试路径：在同进程完成健康门控。
		deps.Probe = &FileHealthProbe{DataDir: h.dataDir, ExpectedVersion: cmd.GetVersion()}
		result := GuardWatch(ctx, cmd, deps, h.currentVersion, cmd.GetJobId())
		h.logger.Info("升级门控结束（同进程）", "state", result.State, "reason", result.Reason)
	}
}

func (h *Handler) newInstaller(state *UpgradeState) Installer {
	// 主重启器：detach restart（systemd-run 独立 cgroup），重启前 spawn guard。
	restarter := NewMainRestarter(state.Unit, h.restartCommand)
	restarter.SpawnGuard = func() error {
		// 关键：guard 用升级前的可信旧副本（Activate 已备份到 BackupPath）启动，
		// 而非刚替换的新版本，保证新版本损坏时 guard 仍能可靠回滚。
		guardBinary := state.BackupPath
		if _, err := os.Stat(guardBinary); err != nil {
			guardBinary = "" // 回退到自身（测试场景）
		}
		return SpawnDetachedGuard(StatePath(h.dataDir), h.configPath, guardBinary)
	}
	// 回滚重启器：guard 已在独立 cgroup，同步执行即可，不再 spawn guard。
	rollbackRestarter := NewRollbackRestarter(state.Unit, h.restartCommand)
	return NewSystemInstaller(state, restarter, rollbackRestarter)
}

func failUpgrade(r Reporter, reason string) {
	r.Report(&pb.UpgradeEvent{
		Status:  pb.UpgradeEvent_STATUS_FAILED,
		Message: reason,
	})
}

// CurrentBinaryPath 返回当前可执行文件的真实路径（解析符号链接）。
func CurrentBinaryPath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(exe)
	if err != nil {
		return exe, nil
	}
	return resolved, nil
}

// SetInProcess 仅供测试：在同进程执行健康门控。
func (h *Handler) SetInProcess() { h.inProcess = true }

var _ control.UpgradeHandler = (*Handler)(nil)
