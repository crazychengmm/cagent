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
	// StateRestarted 表示主进程已完成替换并触发重启，健康门控交由独立 guard。
	StateRestarted State = "restarted"
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

// Run 执行完整编排（主要用于测试：fake Restarter 不会杀死调用进程）。
// 生产中主进程调用 Prepare（会触发重启杀死自己），健康门控由独立 guard 进程执行。
func Run(ctx context.Context, jobID, currentVersion string,
	cmd *pb.UpgradeCommand, pkg []byte, d Deps) Result {
	prepared := Prepare(ctx, jobID, currentVersion, cmd, pkg, d)
	if prepared.State != StateRestarted {
		return prepared
	}
	return GuardWatch(ctx, cmd, d, currentVersion, jobID)
}

// Prepare 执行版本门控、grace、暂存、替换并触发重启。
// 返回 StateRestarted 时调用进程预期很快被服务管理器终止。
func Prepare(ctx context.Context, jobID, currentVersion string,
	cmd *pb.UpgradeCommand, pkg []byte, d Deps) Result {
	report := reporter(d.Reporter, jobID, currentVersion, cmd.GetVersion())
	fail := func(reason string) Result {
		report(pb.UpgradeEvent_STATUS_FAILED, reason)
		return Result{State: StateFailed, Reason: reason}
	}

	grace := d.GraceTimeout
	if grace <= 0 {
		grace = DefaultGraceTimeout
	}

	if err := release.DecideUpgrade(currentVersion, cmd.GetVersion(), cmd.GetAllowDowngrade()); err != nil {
		return fail("版本门控拒绝: " + err.Error())
	}
	if d.Tracker != nil {
		if err := WaitForIdle(ctx, d.Tracker, grace); err != nil {
			return fail("grace 等待失败: " + err.Error())
		}
	}
	staged, err := d.Installer.Stage(pkg)
	if err != nil {
		return fail("暂存失败: " + err.Error())
	}
	if err := d.Installer.Activate(staged); err != nil {
		return fail("替换二进制失败: " + err.Error())
	}

	report(pb.UpgradeEvent_STATUS_INSTALLING, "二进制已替换，重启服务")
	if err := d.Installer.Restart(); err != nil {
		return d.rollback(report, "重启失败: "+err.Error())
	}
	return Result{State: StateRestarted}
}

// GuardWatch 在独立 guard 进程中执行健康门控：通过则提交，失败回滚并重启旧版。
func GuardWatch(ctx context.Context, cmd *pb.UpgradeCommand, d Deps,
	currentVersion, jobID string) Result {
	report := reporter(d.Reporter, jobID, currentVersion, cmd.GetVersion())
	health := d.HealthTimeout
	if health <= 0 {
		health = DefaultHealthTimeout
	}

	hctx, cancel := context.WithTimeout(ctx, health)
	defer cancel()
	if err := d.Probe.WaitHealthy(hctx, health); err != nil {
		return d.rollback(report, "新版本健康检查失败: "+err.Error())
	}
	if err := d.Installer.Commit(); err != nil {
		report(pb.UpgradeEvent_STATUS_SUCCEEDED, "健康通过（旧副本清理失败: "+err.Error()+")")
		return Result{State: StateSucceeded}
	}
	report(pb.UpgradeEvent_STATUS_SUCCEEDED, "新版本健康通过")
	return Result{State: StateSucceeded}
}

func reporter(r Reporter, jobID, from, to string) func(pb.UpgradeEvent_Status, string) {
	return func(status pb.UpgradeEvent_Status, msg string) {
		if r == nil {
			return
		}
		r.Report(&pb.UpgradeEvent{
			JobId:       jobID,
			Status:      status,
			Message:     msg,
			FromVersion: from,
			ToVersion:   to,
		})
	}
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
