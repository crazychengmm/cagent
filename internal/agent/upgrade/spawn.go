package upgrade

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

// releaseDirName 是 agent 数据目录下存放升级工件的子目录。
const releaseDirName = "releases"

// SpawnDetachedGuard 启动独立的升级 guard 进程。
//
// 两个关键设计：
//
//  1. 独立 cgroup：guard 必须在服务重启（systemctl restart cagent.service）期间
//     存活。仅 Setsid 仍处于服务 cgroup，会被 KillMode=control-group 一起杀掉。
//     systemd 环境用 systemd-run 独立 transient unit；非 systemd 回退 Setsid。
//
//  2. 可信二进制：guardBinary 必须指向【升级前的旧版本副本】（cagent.old），
//     绝不能用刚替换上去的新版本——新版本可能启动即崩溃或行为异常（此时 guard
//     自己也是坏的，将永远无法回滚）。guard 用升级前验证过的健康二进制运行，
//     才能在新版本损坏时可靠地把它换回旧版。
func SpawnDetachedGuard(statePath, configPath, guardBinary string) error {
	exe := guardBinary
	if exe == "" {
		var err error
		exe, err = os.Executable()
		if err != nil {
			return fmt.Errorf("定位 guard 二进制: %w", err)
		}
	}
	if _, err := os.Stat(exe); err != nil {
		return fmt.Errorf("guard 二进制不可用 %s: %w", exe, err)
	}
	args := []string{"agent", "--guard-mode", statePath}
	if configPath != "" {
		args = append(args, "--config", configPath)
	}

	if systemdActive() {
		return spawnViaSystemdRun(exe, args)
	}
	return spawnViaSetsid(exe, args, statePath)
}

// systemdActive 判断是否运行在 systemd 引导的系统上。
func systemdActive() bool {
	info, err := os.Stat("/run/systemd/system")
	return err == nil && info.IsDir()
}

// spawnViaSystemdRun 在独立 transient service 中运行 guard。
// systemd-run 默认 service 模式（非 --scope）立即返回，guard 在后台独立
// cgroup 运行；stdout/stderr 进入 journald（unit 名可检索）。
func spawnViaSystemdRun(exe string, agentArgs []string) error {
	unit, err := uniqueGuardUnitName()
	if err != nil {
		return err
	}
	full := []string{"--collect", "--quiet", "--unit", unit, exe}
	full = append(full, agentArgs...)
	cmd := exec.Command("systemd-run", full...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("systemd-run 启动 guard(%s): %w: %s", unit, err, string(out))
	}
	return nil
}

// spawnViaSetsid 回退路径：新会话脱离进程组，不 Wait（非 systemd 环境，如测试）。
func spawnViaSetsid(exe string, agentArgs []string, statePath string) error {
	cmd := exec.Command(exe, agentArgs...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Stdin = nil
	if f, err := os.OpenFile(filepath.Join(filepath.Dir(statePath), "guard.log"),
		os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); err == nil {
		cmd.Stdout = f
		cmd.Stderr = f
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("启动 guard 进程: %w", err)
	}
	return cmd.Process.Release()
}

func uniqueGuardUnitName() (string, error) {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "cagent-upgrade-" + hex.EncodeToString(b[:]) + ".service", nil
}
