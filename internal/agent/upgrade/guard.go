package upgrade

import (
	"context"
	"errors"
	"time"

	"github.com/crazychengmm/cagent/internal/release"
	pb "github.com/crazychengmm/cagent/proto/cagent/v1"
)

// State 是升级编排的状态。
type State string

const (
	StateIdle         State = "idle"
	StateVersionCheck State = "version_check"
	StateWaitingGrace State = "waiting_grace"
	StateStaging      State = "staging"
	StateReplacing    State = "replacing"
	StateHealthCheck  State = "health_check"
	StateSucceeded    State = "succeeded"
	StateRolledBack   State = "rolled_back"
	StateFailed       State = "failed"
)

// 默认时间预算。
const (
	DefaultGraceTimeout  = 2 * time.Minute
	DefaultHealthTimeout = 60 * time.Second
	DefaultTotalTimeout  = 10 * time.Minute
)

// Installer 负责二进制落位、替换、回滚与重启（6.8 提供 systemd 实现）。
type Installer interface {
	// Stage 校验包可执行并放入暂存位置，返回暂存路径。
	Stage(pkg []byte) (string, error)
	// Activate 备份当前二进制并用暂存版本替换。
	Activate(stagedPath string) error
	// Rollback 恢复备份的旧二进制。
	Rollback() error
	// Restart 重启受 systemd 管理的 agent 服务。
	Restart() error
	// Commit 健康确认后清理旧副本。
	Commit() error
}

// HealthProbe 等待新版本通过健康门控。
type HealthProbe interface {
	WaitHealthy(ctx context.Context, timeout time.Duration) error
}

// Reporter 回传升级事件（经控制流）。
type Reporter interface {
	Report(event *pb.UpgradeEvent)
}

// Deps 是状态机的外部依赖。
type Deps struct {
	Tracker       ActiveTracker
	Installer     Installer
	Probe         HealthProbe
	Reporter      Reporter
	GraceTimeout  time.Duration
	HealthTimeout time.Duration
}

// Result 是一次升级的最终结果。
type Result struct {
	State  State
	Reason string
}

// Run 执行升级编排（纯决策，不直接触碰系统）。
// pkg 为已通过三重验真的包内容。
func Run(ctx context.Context, jobID, currentVersion string,
	cmd *pb.UpgradeCommand, pkg []byte, d Deps) Result {
	report := func(status pb.UpgradeEvent_Status, msg string) {
		if d.Reporter != nil {
			d.Reporter.Report(&pb.UpgradeEvent{
				JobId:       jobID,
				Status:      status,
				Message:     msg,
				FromVersion: currentVersion,
				ToVersion:   cmd.GetVersion(),
			})
		}
	}
	fail := func(reason string) Result {
		report(pb.UpgradeEvent_STATUS_FAILED, reason)
		return Result{State: StateFailed, Reason: reason}
	}

	grace := d.GraceTimeout
	if grace <= 0 {
		grace = DefaultGraceTimeout
	}
	health := d.HealthTimeout
	if health <= 0 {
		health = DefaultHealthTimeout
	}

	// 1. 版本门控（防降级）。
	if err := release.DecideUpgrade(currentVersion, cmd.GetVersion(), cmd.GetAllowDowngrade()); err != nil {
		return fail("版本门控拒绝: " + err.Error())
	}

	// 2. grace：等待运行中任务，绝不强杀。
	if d.Tracker != nil {
		if err := WaitForIdle(ctx, d.Tracker, grace); err != nil {
			return fail("grace 等待失败: " + err.Error())
		}
	}

	// 3. 暂存（校验可执行）。
	staged, err := d.Installer.Stage(pkg)
	if err != nil {
		return fail("暂存失败: " + err.Error())
	}

	// 4. 替换（保留旧副本）。
	if err := d.Installer.Activate(staged); err != nil {
		return fail("替换二进制失败: " + err.Error())
	}

	// 5. 重启进入新版本。
	report(pb.UpgradeEvent_STATUS_INSTALLING, "二进制已替换，重启服务")
	if err := d.Installer.Restart(); err != nil {
		// 重启命令本身失败：立即回滚并尝试拉起旧版。
		return d.rollback(report, "重启失败: "+err.Error())
	}

	// 6. 健康门控。
	hctx, cancel := context.WithTimeout(ctx, health)
	defer cancel()
	if err := d.Probe.WaitHealthy(hctx, health); err != nil {
		return d.rollback(report, "新版本健康检查失败: "+err.Error())
	}

	// 7. 健康成功：清理旧副本。
	if err := d.Installer.Commit(); err != nil {
		// 不影响升级成功，仅记录。
		report(pb.UpgradeEvent_STATUS_SUCCEEDED, "健康通过（旧副本清理失败: "+err.Error()+")")
		return Result{State: StateSucceeded}
	}
	report(pb.UpgradeEvent_STATUS_SUCCEEDED, "新版本健康通过")
	return Result{State: StateSucceeded}
}

// rollback 换回旧版、重启、上报回滚事件；返回 rolled_back 结果。
func (d Deps) rollback(report func(pb.UpgradeEvent_Status, string), reason string) Result {
	if err := d.Installer.Rollback(); err != nil {
		report(pb.UpgradeEvent_STATUS_FAILED, reason+"；回滚也失败: "+err.Error())
		return Result{State: StateFailed, Reason: reason + "；回滚失败: " + err.Error()}
	}
	report(pb.UpgradeEvent_STATUS_ROLLED_BACK, reason)
	if err := d.Installer.Restart(); err != nil {
		report(pb.UpgradeEvent_STATUS_FAILED, "回滚后重启失败: "+err.Error())
		return Result{State: StateFailed, Reason: reason + "；回滚后重启失败"}
	}
	return Result{State: StateRolledBack, Reason: reason}
}

// ErrUpgradeCanceled 用于编排被外部取消时的判定。
var ErrUpgradeCanceled = errors.New("升级被取消")
