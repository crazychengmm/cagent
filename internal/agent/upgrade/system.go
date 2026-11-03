package upgrade

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Restarter 触发 agent 服务重启（systemd）。
type Restarter interface {
	Restart() error
}

// SystemdRestarter 触发 agent 服务重启。
//
// 关键约束：agent 主进程运行在 cagent.service 的 cgroup 内，不能同步执行
// `systemctl restart cagent.service`——systemd 以 KillMode=control-group
// 停止服务时会把发起命令的 agent 自身连同 systemctl 子进程一起 SIGTERM
// （实测 exec 返回 "signal: terminated"），与服务启停产生竞争。
// 因此主重启器（detached=true）把 restart 动作放进 systemd-run 的独立
// transient unit（独立 cgroup）延迟执行，agent 不等待自身被杀。
// guard 自身已在独立 cgroup，回滚重启可同步执行（detached=false）。
type SystemdRestarter struct {
	Unit          string
	customCommand string
	detached      bool
	SpawnGuard    func() error
}

// NewMainRestarter 构造主 agent 使用的重启器（detach restart）。
func NewMainRestarter(unit, customCommand string) *SystemdRestarter {
	return &SystemdRestarter{Unit: unit, customCommand: customCommand, detached: true}
}

// NewRollbackRestarter 构造 guard 回滚使用的重启器。
// guard 运行在独立 systemd-run unit（自己的 cgroup），同步执行
// `systemctl restart cagent.service` 不会把 guard 自己杀掉，因此直接同步重启。
func NewRollbackRestarter(unit, customCommand string) *SystemdRestarter {
	return &SystemdRestarter{Unit: unit, customCommand: customCommand, detached: false}
}

func (r *SystemdRestarter) Restart() error {
	if r.detached && r.SpawnGuard != nil {
		if err := r.SpawnGuard(); err != nil {
			return fmt.Errorf("启动 guard: %w", err)
		}
	}

	if r.customCommand != "" {
		// 测试/非 systemd：customCommand 自身负责 detach（如 nohup + kill $PPID）。
		out, err := exec.Command("/bin/sh", "-c", r.customCommand).CombinedOutput()
		if err != nil {
			return fmt.Errorf("自定义重启命令失败: %w: %s", err, string(out))
		}
		return nil
	}

	if r.detached {
		// 生产：在独立 transient unit 中延迟 restart，避免发起者被同 cgroup 杀死。
		return detachedSystemdRestart(r.Unit)
	}
	// guard（独立 cgroup）回滚重启：同步执行安全。
	out, err := exec.Command("systemctl", "restart", r.Unit).CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemctl restart %s: %w: %s", r.Unit, err, string(out))
	}
	return nil
}

// detachedSystemdRestart 通过 systemd-run 在独立 cgroup 的 oneshot unit 中
// 延迟 1 秒重启目标服务；systemd-run 排队后即返回，不阻塞调用方。
func detachedSystemdRestart(unit string) error {
	name, err := uniqueRestartUnitName()
	if err != nil {
		return err
	}
	script := fmt.Sprintf("sleep 1; systemctl restart %s", unit)
	cmd := exec.Command("systemd-run", "--collect", "--quiet",
		"--unit", name, "/bin/sh", "-c", script)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("systemd-run 调度重启失败: %w: %s", err, string(out))
	}
	return nil
}

func uniqueRestartUnitName() (string, error) {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "cagent-restart-" + hex.EncodeToString(b[:]), nil
}

// SystemInstaller 用复制+rename 实现二进制替换，兼容跨文件系统。
type SystemInstaller struct {
	state             *UpgradeState
	restarter         Restarter
	rollbackRestarter Restarter
}

// NewSystemInstaller 构造安装器；rollbackRestarter 可为 nil（回滚时用 restarter）。
func NewSystemInstaller(state *UpgradeState, restarter Restarter, rollbackRestarter ...Restarter) *SystemInstaller {
	i := &SystemInstaller{state: state, restarter: restarter}
	if len(rollbackRestarter) > 0 {
		i.rollbackRestarter = rollbackRestarter[0]
	}
	return i
}

// Stage 从验签通过的 tar.gz 包中解出二进制，写入暂存位置并置为可执行。
func (i *SystemInstaller) Stage(pkg []byte) (string, error) {
	if len(pkg) == 0 {
		return "", fmt.Errorf("升级包为空")
	}
	binary, err := ExtractBinary(pkg)
	if err != nil {
		return "", fmt.Errorf("解包失败: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(i.state.StagedPath), 0o700); err != nil {
		return "", err
	}
	if err := writeExecutable(i.state.StagedPath, binary); err != nil {
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

// Rollback 恢复备份。替换完成后 fsync 目录，确保 restart 一定读到旧版。
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
	// 关键：fsync 二进制所在目录，保证 rename 落盘，restart 读到旧版。
	if dir, err := os.Open(filepath.Dir(i.state.BinaryPath)); err == nil {
		_ = dir.Sync()
		_ = dir.Close()
	}
	// 同步确认磁盘版本确为旧版（guard 与服务分属不同 cgroup，可安全执行）。
	if out, err := exec.Command(i.state.BinaryPath, "--version").CombinedOutput(); err == nil {
		rollbackTrace(i.state, "回滚后磁盘二进制: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

// rollbackTrace 写回滚诊断到 dataDir/rollback-trace.log。
func rollbackTrace(state *UpgradeState, format string, a ...any) {
	f, err := os.OpenFile(filepath.Join(filepath.Dir(state.StagedPath), "..", "rollback-trace.log"),
		os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, format+"\n", a...)
}

// Restart 重启服务（生产由 systemd 完成；首次重启前已 detach guard）。
func (i *SystemInstaller) Restart() error {
	return i.restarter.Restart()
}

// RestartForRollback 回滚后的重启：guard 已在运行，不再 spawn 新 guard。
func (i *SystemInstaller) RestartForRollback() error {
	if i.rollbackRestarter != nil {
		return i.rollbackRestarter.Restart()
	}
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
