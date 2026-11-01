package upgrade

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStateSaveLoadClear(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "upgrade-state.json")
	st := &UpgradeState{
		JobID: "j-1", FromVersion: "1.0.0", ToVersion: "2.0.0",
		BinaryPath: "/usr/local/bin/cagent", Unit: "cagent.service",
	}
	if err := SaveState(path, st); err != nil {
		t.Fatal(err)
	}
	info, _ := filepath.Glob(filepath.Join(dir, "*.tmp"))
	if len(info) != 0 {
		t.Error("不应残留临时文件")
	}
	loaded, err := LoadState(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ToVersion != "2.0.0" || loaded.JobID != "j-1" {
		t.Errorf("状态往返错误: %+v", loaded)
	}
	if err := ClearState(path); err != nil {
		t.Fatal(err)
	}
	if err := ClearState(path); err != nil {
		t.Error("重复删除应幂等")
	}
	if _, err := LoadState(path); !os.IsNotExist(err) {
		t.Errorf("删除后应返回 not-exist，实际 %v", err)
	}
}

func TestHealthyMarker(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if IsHealthy(dir, "1.0.0") {
		t.Fatal("初始不应健康")
	}
	if err := MarkHealthy(dir, "2.0.0"); err != nil {
		t.Fatal(err)
	}
	if !IsHealthy(dir, "2.0.0") {
		t.Error("写标记后应健康")
	}
	if IsHealthy(dir, "1.0.0") {
		t.Error("版本不匹配不应判健康")
	}
	ClearHealthy(dir)
	if IsHealthy(dir, "2.0.0") {
		t.Error("清理后不应健康")
	}
}

func TestFileHealthProbe(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	probe := &FileHealthProbe{DataDir: dir, ExpectedVersion: "2.0.0"}

	go func() {
		time.Sleep(200 * time.Millisecond)
		_ = MarkHealthy(dir, "2.0.0")
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := probe.WaitHealthy(ctx, 2*time.Second); err != nil {
		t.Fatalf("应等待到健康: %v", err)
	}

	probe2 := &FileHealthProbe{DataDir: dir, ExpectedVersion: "9.9.9"}
	ctx2, cancel2 := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel2()
	if err := probe2.WaitHealthy(ctx2, 400*time.Millisecond); err == nil {
		t.Fatal("版本不符应超时")
	}
}
