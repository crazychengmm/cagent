package upgrade

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

// releaseDirName 是 agent 数据目录下存放升级工件的子目录。
const releaseDirName = "releases"

// SpawnDetachedGuard 启动脱离当前进程组的独立 guard（新会话），
// 随后主进程可被 systemctl restart 终止而不影响 guard。
func SpawnDetachedGuard(statePath, configPath string) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("定位自身二进制: %w", err)
	}
	args := []string{"agent", "--guard-mode", statePath}
	if configPath != "" {
		args = append(args, "--config", configPath)
	}
	cmd := exec.Command(exe, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Stdin = nil
	// 输出重定向到数据目录下的 guard 日志（失败不阻断升级）。
	if f, err := os.OpenFile(filepath.Join(filepath.Dir(statePath), "guard.log"),
		os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); err == nil {
		cmd.Stdout = f
		cmd.Stderr = f
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("启动 guard 进程: %w", err)
	}
	// 不 Wait：guard 独立运行。
	return cmd.Process.Release()
}
