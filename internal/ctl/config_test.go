package ctl

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// 注意：使用 t.Setenv 的用例不能 t.Parallel（会修改进程环境）。
func TestLoadConfig_FileAndEnv(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ctl.yaml")
	if err := os.WriteFile(path, []byte(
		"admin_address: \"127.0.0.1:9445\"\nadmin_token: \"file-token\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AdminAddress != "127.0.0.1:9445" || cfg.AdminToken != "file-token" {
		t.Errorf("文件加载错误: %+v", cfg)
	}

	t.Setenv(EnvAdminAddress, "env.example:9445")
	t.Setenv(EnvAdminToken, "env-token")
	cfg2, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg2.AdminAddress != "env.example:9445" || cfg2.AdminToken != "env-token" {
		t.Errorf("环境变量应覆盖文件: %+v", cfg2)
	}
}

func TestLoadConfig_MissingFileOptional(t *testing.T) {
	t.Setenv(EnvAdminAddress, "x:1")
	t.Setenv(EnvAdminToken, "t")
	cfg, err := LoadConfig(filepath.Join(t.TempDir(), "absent.yaml"))
	if err != nil {
		t.Fatalf("配置文件缺失应回退环境变量: %v", err)
	}
	if cfg.AdminAddress != "x:1" {
		t.Errorf("地址错误: %q", cfg.AdminAddress)
	}
}

func TestDial_MissingCredentials(t *testing.T) {
	t.Parallel()

	cases := []*Config{
		{AdminToken: "t"},
		{AdminAddress: "127.0.0.1:1"},
	}
	for i, cfg := range cases {
		if _, err := cfg.Dial(context.Background()); err == nil {
			t.Errorf("用例 %d 缺凭据应报错", i)
		}
	}
}

func TestSplitConfigFlag(t *testing.T) {
	t.Parallel()

	path, rest := splitConfigFlag([]string{
		"job", "run", "--config", "/tmp/c.yaml", "--selector", "env=prod", "uptime",
	})
	if path != "/tmp/c.yaml" {
		t.Errorf("path=%q", path)
	}
	want := []string{"job", "run", "--selector", "env=prod", "uptime"}
	if len(rest) != len(want) {
		t.Fatalf("rest=%v", rest)
	}
	for i := range want {
		if rest[i] != want[i] {
			t.Errorf("rest[%d]=%q 期望 %q", i, rest[i], want[i])
		}
	}

	path2, rest2 := splitConfigFlag([]string{"agent", "list", "--config=/c.yaml"})
	if path2 != "/c.yaml" || len(rest2) != 2 {
		t.Errorf("等号形式解析错误: %q %v", path2, rest2)
	}
}

func TestRun_UnknownAndHelp(t *testing.T) {
	t.Parallel()

	var out, errBuf fakeBuffer
	if code := Run([]string{"nope"}, &out, &errBuf); code != 2 {
		t.Errorf("未知子命令退出码=%d", code)
	}
	if code := Run([]string{"help"}, &out, &errBuf); code != 0 {
		t.Errorf("help 退出码=%d", code)
	}
	if code := Run(nil, &out, &errBuf); code != 2 {
		t.Errorf("无子命令退出码=%d", code)
	}
}

type fakeBuffer struct{ b []byte }

func (f *fakeBuffer) Write(p []byte) (int, error) {
	f.b = append(f.b, p...)
	return len(p), nil
}
