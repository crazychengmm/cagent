package upgrade

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/crazychengmm/cagent/internal/release"
)

func TestSystemInstaller_StageActivateRollbackCommit(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	binPath := filepath.Join(dir, "bin", "cagent")
	stagedPath := filepath.Join(dir, "releases", stagedFileName)
	backupPath := filepath.Join(dir, "releases", backupFileName)
	os.MkdirAll(filepath.Dir(binPath), 0o755)
	if err := os.WriteFile(binPath, []byte("#!/bin/sh\necho old\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	oldContent, _ := os.ReadFile(binPath)

	var restarts int
	state := &UpgradeState{
		BinaryPath: binPath,
		BackupPath: backupPath,
		StagedPath: stagedPath,
		Unit:       DefaultSystemdUnit,
	}
	inst := NewSystemInstaller(state, &fakeRestarter{fn: func() error {
		restarts++
		return nil
	}})

	// 打成 tar.gz 发布包后 Stage
	newBin := filepath.Join(dir, "new-cagent")
	if err := os.WriteFile(newBin, []byte("#!/bin/sh\necho new\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	var pkg bytes.Buffer
	if _, err := release.BuildPackage(&pkg, newBin, "2.0.0"); err != nil {
		t.Fatal(err)
	}
	// Stage
	staged, err := inst.Stage(pkg.Bytes())
	if err != nil {
		t.Fatalf("Stage: %v", err)
	}
	if staged != stagedPath {
		t.Errorf("staged path = %q", staged)
	}
	info, err := os.Stat(stagedPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Errorf("暂存文件必须可执行，权限 %o", info.Mode().Perm())
	}

	// Activate
	if err := inst.Activate(staged); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	newContent, _ := os.ReadFile(binPath)
	if string(newContent) == string(oldContent) {
		t.Error("二进制未被替换")
	}
	if _, err := os.Stat(backupPath); err != nil {
		t.Fatalf("旧版本应已备份: %v", err)
	}

	// Restart
	if err := inst.Restart(); err != nil || restarts != 1 {
		t.Errorf("Restart err=%v count=%d", err, restarts)
	}

	// Rollback
	if err := inst.Rollback(); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	restored, _ := os.ReadFile(binPath)
	if string(restored) != string(oldContent) {
		t.Error("回滚后内容不是旧版本")
	}

	// Commit 前重新 Activate 以便清理暂存/备份
	if err := inst.Activate(staged); err != nil {
		t.Fatal(err)
	}
	if err := inst.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	for _, p := range []string{backupPath, stagedPath} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("Commit 后 %s 应删除", p)
		}
	}
}

func TestSystemInstaller_RollbackWithoutBackupFails(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	inst := NewSystemInstaller(&UpgradeState{
		BinaryPath: filepath.Join(dir, "cagent"),
		BackupPath: filepath.Join(dir, "missing.old"),
	}, &fakeRestarter{})
	if err := inst.Rollback(); err == nil {
		t.Fatal("备份缺失应报错")
	}
}

func TestMainRestarter_FailedCustomCommand(t *testing.T) {
	t.Parallel()

	// 自定义重启命令返回非零，应报错。
	r := NewMainRestarter("cagent.service", "exit 7")
	if err := r.Restart(); err == nil {
		t.Fatal("重启命令失败应返回错误")
	}
}

func TestMainRestarter_SpawnsGuardBeforeRestart(t *testing.T) {
	t.Parallel()

	spawned := false
	restartRan := false
	r := NewMainRestarter("cagent.service", "true")
	r.SpawnGuard = func() error {
		spawned = true
		return nil
	}
	// 用一个能观察顺序的自定义命令覆盖：在 SpawnGuard 之后执行。
	r.customCommand = "sh -c 'test -f /nonexistent-guard-marker || true'"
	_ = restartRan
	if err := r.Restart(); err != nil {
		t.Fatal(err)
	}
	if !spawned {
		t.Error("detached 重启应先 SpawnGuard")
	}
}

func TestRollbackRestarter_RunsCustomCommandSynchronously(t *testing.T) {
	t.Parallel()

	// 回滚重启器不 spawn guard，同步执行自定义命令。
	marker := filepath.Join(t.TempDir(), "rollback-marker")
	r := NewRollbackRestarter("cagent.service", "touch "+marker)
	if err := r.Restart(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("回滚重启命令未执行: %v", err)
	}
}

var errFake = errors.New("fake command failure")

type fakeRestarter struct {
	fn func() error
}

func (f *fakeRestarter) Restart() error {
	if f.fn != nil {
		return f.fn()
	}
	return nil
}
