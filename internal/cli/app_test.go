package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("写临时文件失败: %v", err)
	}
	return path
}

func TestRun_NoArgs(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer
	if code := Run(nil, &stdout, &stderr); code != 2 {
		t.Errorf("无参数退出码 = %d, 期望 2", code)
	}
	if stderr.Len() == 0 {
		t.Error("无参数应向 stderr 打印用法")
	}
}

func TestRun_VersionAndHelp(t *testing.T) {
	t.Parallel()

	for _, arg := range []string{"--version", "-v"} {
		var stdout, stderr bytes.Buffer
		if code := Run([]string{arg}, &stdout, &stderr); code != 0 {
			t.Errorf("%s 退出码 = %d, 期望 0", arg, code)
		}
		if !strings.Contains(stdout.String(), "cagent version") {
			t.Errorf("%s 未输出版本，实际: %q", arg, stdout.String())
		}
	}

	var stdout, stderr bytes.Buffer
	if code := Run([]string{"--help"}, &stdout, &stderr); code != 0 || !strings.Contains(stdout.String(), "用法") {
		t.Errorf("help 退出码=%d, 输出=%q", code, stdout.String())
	}
}

func TestRun_UnknownSubcommand(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer
	if code := Run([]string{"daemon"}, &stdout, &stderr); code != 2 {
		t.Errorf("未知子命令退出码 = %d, 期望 2", code)
	}
}

func TestRun_Agent(t *testing.T) {
	t.Parallel()

	t.Run("合法配置启动占位", func(t *testing.T) {
		t.Parallel()
		path := writeFile(t, "agent.yaml", `
control_address: "h:9444"
labels:
  env: prod
`)
		var stdout, stderr bytes.Buffer
		code := Run([]string{"agent", "--config", path}, &stdout, &stderr)
		if code != 0 {
			t.Fatalf("退出码 = %d, stderr = %s", code, stderr.String())
		}
		if !strings.Contains(stderr.String(), `"msg":"agent 启动"`) ||
			!strings.Contains(stderr.String(), `"control_address":"h:9444"`) {
			t.Errorf("未输出结构化启动日志: %s", stderr.String())
		}
		if strings.Contains(stderr.String(), "enrollment_token") {
			t.Error("日志不得包含 enrollment_token 字段（C-LOG-05）")
		}
	})

	t.Run("配置文件缺失", func(t *testing.T) {
		t.Parallel()
		var stdout, stderr bytes.Buffer
		code := Run([]string{"agent", "--config", filepath.Join(t.TempDir(), "no.yaml")}, &stdout, &stderr)
		if code != 1 {
			t.Errorf("缺配置退出码 = %d, 期望 1", code)
		}
	})

	t.Run("非法配置", func(t *testing.T) {
		t.Parallel()
		path := writeFile(t, "bad.yaml", `data_dir: "/tmp/x"`+"\n") // 缺 control_address
		var stdout, stderr bytes.Buffer
		if code := Run([]string{"agent", "--config", path}, &stdout, &stderr); code != 1 {
			t.Errorf("非法配置退出码 = %d, 期望 1", code)
		}
	})

	t.Run("未知 flag", func(t *testing.T) {
		t.Parallel()
		var stdout, stderr bytes.Buffer
		if code := Run([]string{"agent", "--bogus"}, &stdout, &stderr); code != 2 {
			t.Errorf("未知 flag 退出码 = %d, 期望 2", code)
		}
	})
}

func TestRun_ServerConfigErrors(t *testing.T) {
	t.Parallel()

	// 合法配置会真实启动三个监听端口并阻塞到收到信号，
	// 成功路径由 internal/server/serve 的集成测试覆盖，这里只测启动前失败。
	t.Run("配置缺失", func(t *testing.T) {
		t.Parallel()
		var stdout, stderr bytes.Buffer
		code := Run([]string{"server", "--config", filepath.Join(t.TempDir(), "no.yaml")}, &stdout, &stderr)
		if code != 1 {
			t.Errorf("缺配置退出码 = %d, 期望 1", code)
		}
	})
	t.Run("非法配置", func(t *testing.T) {
		t.Parallel()
		path := writeFile(t, "bad-server.yaml", "enrollment_listen: \":9444\"\ncontrol_listen: \":9444\"\n")
		var stdout, stderr bytes.Buffer
		code := Run([]string{"server", "--config", path}, &stdout, &stderr)
		if code != 1 {
			t.Errorf("监听重叠配置退出码 = %d, 期望 1", code)
		}
	})
}

func TestRun_CtlPlaceholder(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer
	if code := Run([]string{"ctl"}, &stdout, &stderr); code != 2 {
		t.Errorf("ctl 占位退出码 = %d, 期望 2", code)
	}
}
