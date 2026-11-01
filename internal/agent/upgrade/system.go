package upgrade

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
)

// Restarter 触发 agent 服务重启（systemd）。
type Restarter interface {
	Restart() error
}

// SystemdRestarter 通过 systemctl 重启单元；重启前可 detach 一个 guard 进程。
type SystemdRestarter struct {
	Unit       string
	SpawnGuard func() error
	Runner     func(name string, args ...string) ([]byte, error)
}

// NewSystemdRestarter 构造默认 systemctl 重启器。
func NewSystemdRestarter(unit string) *SystemdRestarter {
	return &SystemdRestarter{
		Unit: unit,
		Runner: func(name string, args ...string) ([]byte, error) {
			return exec.Command(name, args...).CombinedOutput()
		},
	}
}

func (r *SystemdRestarter) Restart() error {
	if r.SpawnGuard != nil {
		if err := r.SpawnGuard(); err != nil {
			return fmt.Errorf("启动 guard: %w", err)
		}
	}
	out, err := r.Runner("systemctl", "restart", r.Unit)
	if err != nil {
		return fmt.Errorf("systemctl restart %s: %w: %s", r.Unit, err, string(out))
	}
	return nil
}

// SystemInstaller 用复制+rename 实现二进制替换，兼容跨文件系统。
type SystemInstaller struct {
	state     *UpgradeState
	restarter Restarter
}

// NewSystemInstaller 构造安装器。
func NewSystemInstaller(state *UpgradeState, restarter Restarter) *SystemInstaller {
	return &SystemInstaller{state: state, restarter: restarter}
}

// Stage 把验签通过的包写入暂存位置并置为可执行。
func (i *SystemInstaller) Stage(pkg []byte) (string, error) {
	if len(pkg) == 0 {
		return "", fmt.Errorf("升级包为空")
	}
	if err := os.MkdirAll(filepath.Dir(i.state.StagedPath), 0o700); err != nil {
		return "", err
	}
	if err := writeExecutable(i.state.StagedPath, pkg); err != nil {
		return "", err
	}
	return i.state.StagedPath, nil
}

// Activate 备份当前二进制并用暂存版本原子替换。
func (i *SystemInstaller) Activate(stagedPath string) error {
	if err := copyFile(i.state.BinaryPath, i.state.BackupPath, 0o755); err != nil {
		return fmt.Errorf("备份旧版本: %w", err)
	}
	newPath := i.state.BinaryPath + ".new"
	if err := copyFile(stagedPath, newPath, 0o755); err != nil {
		return fmt.Errorf("写入新版本: %w", err)
	}
	if err := os.Rename(newPath, i.state.BinaryPath); err != nil {
		_ = os.Remove(newPath)
		return fmt.Errorf("替换二进制: %w", err)
	}
	return nil
}

// Rollback 恢复备份。
func (i *SystemInstaller) Rollback() error {
	if i.state.BackupPath == "" {
		return fmt.Errorf("无备份路径")
	}
	if _, err := os.Stat(i.state.BackupPath); err != nil {
		return fmt.Errorf("旧版本副本缺失: %w", err)
	}
	newPath := i.state.BinaryPath + ".rollback"
	if err := copyFile(i.state.BackupPath, newPath, 0o755); err != nil {
		return err
	}
	if err := os.Rename(newPath, i.state.BinaryPath); err != nil {
		return fmt.Errorf("回滚替换失败: %w", err)
	}
	return nil
}

// Restart 重启服务（生产由 systemd 完成；guard 进程在重启前已 detach）。
func (i *SystemInstaller) Restart() error {
	return i.restarter.Restart()
}

// Commit 删除旧副本（健康确认后）。
func (i *SystemInstaller) Commit() error {
	if err := os.Remove(i.state.BackupPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	return os.Remove(i.state.StagedPath)
}

func writeExecutable(path string, data []byte) error {
	if err := os.WriteFile(path, data, 0o755); err != nil {
		return err
	}
	return os.Chmod(path, 0o755)
}

func copyFile(src, dst string, perm os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".copy-tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, perm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dst)
}
