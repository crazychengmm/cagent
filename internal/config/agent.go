package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"regexp"
	"time"

	"gopkg.in/yaml.v3"
)

// Agent 侧默认值（对应 specs/agent-connection、remote-job-execution）。
const (
	defaultAgentDataDir        = "/var/lib/cagent"
	defaultHeartbeatInterval   = 15 * time.Second
	defaultAgentJobConcurrency = 4
	defaultAgentJobTimeout     = 5 * time.Minute
	maxAgentJobTimeout         = 60 * time.Minute
	maxAgentJobConcurrency     = 64
	defaultAgentConfigPath     = "/etc/cagent/config.yaml"
)

var labelKeyPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,62}$`)

// AgentConfig 是 /etc/cagent/config.yaml 的解析结果。
type AgentConfig struct {
	// EnrollmentAddress 注册端口（host:9443），仅首次注册需要。
	EnrollmentAddress string `yaml:"enrollment_address"`
	// ControlAddress 控制端口（host:9444），mTLS 长连接目标，必填。
	ControlAddress string `yaml:"control_address"`
	// EnrollmentToken 引导令牌，仅首次注册使用；已注册后可为空。
	EnrollmentToken string `yaml:"enrollment_token"`
	// ServerCAFile server CA 信任锚点 PEM 路径（与 ServerCAFingerprint 二选一，
	// 仅首次注册前必需；注册成功后 CA 随身份持久化）。
	ServerCAFile string `yaml:"server_ca_file"`
	// ServerCAFingerprint 服务器证书 SHA256 指纹（十六进制），与文件锚点二选一。
	ServerCAFingerprint string `yaml:"server_ca_fingerprint"`
	// DataDir 身份、证书、旧版本副本与升级状态目录。
	DataDir string `yaml:"data_dir"`
	// Labels 静态标签全集，随心跳上报。
	Labels map[string]string `yaml:"labels"`

	HeartbeatInterval Duration `yaml:"heartbeat_interval"`
	// JobConcurrency 单 agent 并发执行 job 的 worker 数。
	JobConcurrency int `yaml:"job_concurrency"`
	// DefaultJobTimeout 未显式指定超时时的 job 超时；硬上限 60m。
	DefaultJobTimeout Duration `yaml:"default_job_timeout"`
}

// DefaultAgentConfig 返回带全部默认值的 agent 配置。
func DefaultAgentConfig() *AgentConfig {
	return &AgentConfig{
		DataDir:           defaultAgentDataDir,
		Labels:            map[string]string{},
		HeartbeatInterval: Duration(defaultHeartbeatInterval),
		JobConcurrency:    defaultAgentJobConcurrency,
		DefaultJobTimeout: Duration(defaultAgentJobTimeout),
	}
}

// LoadAgentConfig 从 path 读取 YAML：先置默认值再覆盖，未知字段直接报错。
func LoadAgentConfig(path string) (*AgentConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取 agent 配置 %s: %w", path, err)
	}

	cfg := DefaultAgentConfig()
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(cfg); err != nil {
		return nil, fmt.Errorf("解析 agent 配置 %s: %w", path, err)
	}

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("agent 配置校验失败: %w", err)
	}
	return cfg, nil
}

// Validate 检查静态约束。首次注册三要素的条件性校验（无身份文件时必填）
// 属于运行期逻辑，在注册流程中处理。
func (c *AgentConfig) Validate() error {
	if c.ControlAddress == "" {
		return errors.New("control_address 必填")
	}
	if c.DataDir == "" {
		return errors.New("data_dir 不能为空")
	}
	if c.HeartbeatInterval.Std() <= 0 {
		return errors.New("heartbeat_interval 必须为正数")
	}
	if c.JobConcurrency < 1 || c.JobConcurrency > maxAgentJobConcurrency {
		return fmt.Errorf("job_concurrency 必须在 1..%d 之间", maxAgentJobConcurrency)
	}
	timeout := c.DefaultJobTimeout.Std()
	if timeout <= 0 {
		return errors.New("default_job_timeout 必须为正数")
	}
	if timeout > maxAgentJobTimeout {
		return fmt.Errorf("default_job_timeout 不能超过 %s", maxAgentJobTimeout)
	}
	for key, value := range c.Labels {
		if !labelKeyPattern.MatchString(key) {
			return fmt.Errorf("标签键 %q 非法（字母数字开头，仅含字母数字._-，≤63）", key)
		}
		if value == "" {
			return fmt.Errorf("标签 %q 的值不能为空", key)
		}
	}
	return nil
}

// DefaultAgentConfigPath 返回标准安装路径。
func DefaultAgentConfigPath() string {
	return defaultAgentConfigPath
}
