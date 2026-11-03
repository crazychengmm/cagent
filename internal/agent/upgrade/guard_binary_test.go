package upgrade

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSpawnGuard_UsesTrustedOldBinary 防御回归：guard 必须由升级前的旧副本
// 启动，而不是刚替换上去的新版本——否则新版本损坏时 guard 自己也坏，无法回滚。
func TestSpawnGuard_UsesTrustedOldBinary(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	trusted := filepath.Join(dir, "cagent.old")
	newBin := filepath.Join(dir, "cagent")

	// 旧副本可执行，新二进制"损坏"（空文件/不可执行）
	if err := os.WriteFile(trusted, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newBin, []byte("broken"), 0o644); err != nil {
		t.Fatal(err)
	}

	statePath := filepath.Join(dir, "upgrade-state.json")
	cfgPath := filepath.Join(dir, "config.yaml")
	_ = os.WriteFile(cfgPath, []byte("data_dir: "+dir+"\n"), 0o600)

	// 非 systemd 环境走 Setsid；指定 trusted 旧副本应被接受（二进制存在），
	// 而若误用损坏的新二进制则会在启动时报错。
	err := SpawnDetachedGuard(statePath, cfgPath, trusted)
	if err != nil {
		t.Fatalf("用可信旧副本启动 guard 失败: %v", err)
	}

	// 显式指定一个不存在的 guard 二进制应报错。
	if err := SpawnDetachedGuard(statePath, cfgPath, filepath.Join(dir, "missing")); err == nil {
		t.Fatal("不存在的 guard 二进制应报错")
	}

	// 传空路径回退到 os.Executable（当前测试进程，必然存在），不应报错。
	if err := SpawnDetachedGuard(statePath, cfgPath, ""); err != nil {
		t.Fatalf("空路径回退应使用当前可执行文件: %v", err)
	}
}

// TestNewInstaller_GuardBinaryIsBackup 确保 handler 构造时 guard 取 BackupPath。
func TestNewInstaller_GuardBinaryIsBackup(t *testing.T) {
	t.Parallel()
	// 该约束由 handler.newInstaller 中的 SpawnGuard 闭包保证，
	// 这里通过源码层面的字符串检查防御被误改。
	src, err := os.ReadFile("handler.go")
	if err != nil {
		t.Skip("handler.go 不可读")
	}
	if !strings.Contains(string(src), "guardBinary := state.BackupPath") {
		t.Error("SpawnGuard 必须使用 state.BackupPath 作为 guard 二进制")
	}
}
