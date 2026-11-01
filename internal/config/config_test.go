package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeTempConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("写临时配置失败: %v", err)
	}
	return path
}

func TestLoadAgentConfig_DefaultsAndOverrides(t *testing.T) {
	t.Parallel()

	path := writeTempConfig(t, `
control_address: "agent.example:9444"
labels:
  env: prod
  role: db
`)
	cfg, err := LoadAgentConfig(path)
	if err != nil {
		t.Fatalf("加载配置失败: %v", err)
	}

	if cfg.ControlAddress != "agent.example:9444" {
		t.Errorf("ControlAddress = %q, 期望 agent.example:9444", cfg.ControlAddress)
	}
	if cfg.DataDir != defaultAgentDataDir {
		t.Errorf("DataDir 默认值 = %q, 期望 %q", cfg.DataDir, defaultAgentDataDir)
	}
	if cfg.HeartbeatInterval.Std() != 15*time.Second {
		t.Errorf("HeartbeatInterval = %s, 期望 15s", cfg.HeartbeatInterval.Std())
	}
	if cfg.JobConcurrency != 4 {
		t.Errorf("JobConcurrency = %d, 期望默认 4", cfg.JobConcurrency)
	}
	if cfg.DefaultJobTimeout.Std() != 5*time.Minute {
		t.Errorf("DefaultJobTimeout = %s, 期望 5m", cfg.DefaultJobTimeout.Std())
	}
	if cfg.Labels["env"] != "prod" || cfg.Labels["role"] != "db" {
		t.Errorf("Labels = %v, 期望 env=prod,role=db", cfg.Labels)
	}
}

func TestLoadAgentConfig_UnknownFieldRejected(t *testing.T) {
	t.Parallel()

	path := writeTempConfig(t, `
control_address: "h:9444"
heatbeat_interval: "10s"
`)
	if _, err := LoadAgentConfig(path); err == nil {
		t.Fatal("拼写错误的未知字段应报错，实际返回 nil")
	}
}

func TestLoadAgentConfig_MissingFile(t *testing.T) {
	t.Parallel()

	if _, err := LoadAgentConfig(filepath.Join(t.TempDir(), "absent.yaml")); err == nil {
		t.Fatal("配置文件不存在应返回错误")
	}
}

func TestAgentConfig_Validate(t *testing.T) {
	t.Parallel()

	valid := func() AgentConfig {
		c := DefaultAgentConfig()
		c.ControlAddress = "h:9444"
		return *c
	}

	tests := []struct {
		name    string
		mutate  func(*AgentConfig)
		wantErr bool
	}{
		{"合法默认配置", func(c *AgentConfig) {}, false},
		{"缺 control_address", func(c *AgentConfig) { c.ControlAddress = "" }, true},
		{"心跳间隔为零", func(c *AgentConfig) { c.HeartbeatInterval = 0 }, true},
		{"并发为零", func(c *AgentConfig) { c.JobConcurrency = 0 }, true},
		{"并发超过上限", func(c *AgentConfig) { c.JobConcurrency = maxAgentJobConcurrency + 1 }, true},
		{"超时超过 60m", func(c *AgentConfig) { c.DefaultJobTimeout = Duration(maxAgentJobTimeout + time.Second) }, true},
		{"标签键非法", func(c *AgentConfig) { c.Labels = map[string]string{"bad key": "v"} }, true},
		{"标签值为空", func(c *AgentConfig) { c.Labels = map[string]string{"env": ""} }, true},
		{"合法标签", func(c *AgentConfig) { c.Labels = map[string]string{"env.1": "prod-a"} }, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := valid()
			tt.mutate(&cfg)
			err := cfg.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() 错误 = %v, wantErr = %v", err, tt.wantErr)
			}
		})
	}
}

func TestLoadServerConfig_Defaults(t *testing.T) {
	t.Parallel()

	path := writeTempConfig(t, `data_dir: "/tmp/cagent-server"`)
	cfg, err := LoadServerConfig(path)
	if err != nil {
		t.Fatalf("加载 server 配置失败: %v", err)
	}
	if cfg.EnrollmentListen != ":9443" || cfg.ControlListen != ":9444" || cfg.AdminListen != ":9445" {
		t.Errorf("监听默认值错误: %+v", cfg)
	}
	if cfg.HeartbeatTimeout.Std() != 45*time.Second {
		t.Errorf("HeartbeatTimeout = %s, 期望 45s", cfg.HeartbeatTimeout.Std())
	}
	if cfg.QueueTTL.Std() != 24*time.Hour {
		t.Errorf("QueueTTL = %s, 期望 24h", cfg.QueueTTL.Std())
	}
}

func TestServerConfig_ValidateRejectsDuplicateListen(t *testing.T) {
	t.Parallel()

	cfg := DefaultServerConfig()
	cfg.AdminListen = cfg.ControlListen
	if err := cfg.Validate(); err == nil {
		t.Fatal("三个监听地址重叠应报错")
	}
}

func TestDuration_UnmarshalYAML(t *testing.T) {
	t.Parallel()

	path := writeTempConfig(t, `
control_address: "h:9444"
heartbeat_interval: "30s"
default_job_timeout: "2m"
`)
	cfg, err := LoadAgentConfig(path)
	if err != nil {
		t.Fatalf("加载配置失败: %v", err)
	}
	if cfg.HeartbeatInterval.Std() != 30*time.Second {
		t.Errorf("HeartbeatInterval = %s, 期望 30s", cfg.HeartbeatInterval.Std())
	}
	if cfg.DefaultJobTimeout.Std() != 2*time.Minute {
		t.Errorf("DefaultJobTimeout = %s, 期望 2m", cfg.DefaultJobTimeout.Std())
	}
}

func TestDuration_InvalidText(t *testing.T) {
	t.Parallel()

	path := writeTempConfig(t, `
control_address: "h:9444"
heartbeat_interval: "soon"
`)
	if _, err := LoadAgentConfig(path); err == nil {
		t.Fatal("非法时长字符串应报错")
	}
}
