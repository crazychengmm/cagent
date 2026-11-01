package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// Server 侧默认值与监听端口约定。
const (
	defaultEnrollmentListen   = ":9443"
	defaultControlListen      = ":9444"
	defaultAdminListen        = ":9445"
	defaultServerDataDir      = "/var/lib/cagent-server"
	defaultServerConfigPath   = "/etc/cagent-server/config.yaml"
	defaultHeartbeatTimeout   = 45 * time.Second
	defaultQueueTTL           = 24 * time.Hour
	defaultAgentCertValidity  = 90 * 24 * time.Hour
	defaultEnrollmentTokenTTL = 7 * 24 * time.Hour
	// ServerCertValidity 是首启生成的 server 叶子证书有效期（删文件重签轮换）。
	ServerCertValidity = 10 * 365 * 24 * time.Hour
)

// 默认的 server 证书 SAN（生产安装应在配置中追加实际访问主机名/IP）。
var defaultServerCertHosts = []string{"localhost", "127.0.0.1"}

// ServerConfig 是 /etc/cagent-server/config.yaml 的解析结果。
type ServerConfig struct {
	// EnrollmentListen 注册端口，单向 TLS + token。
	EnrollmentListen string `yaml:"enrollment_listen"`
	// ControlListen mTLS 控制端口。
	ControlListen string `yaml:"control_listen"`
	// AdminListen ctl 管理 API 端口（admin token 鉴权）。
	AdminListen string `yaml:"admin_listen"`
	// DataDir CA 根密钥、SQLite、admin token 的存放目录。
	DataDir string `yaml:"data_dir"`
	// ServerCertHosts 首启生成的 server 叶子证书 SAN（DNS 或 IP）。
	ServerCertHosts []string `yaml:"server_cert_hosts"`
	// ReleaseSigningPubkeyFile 发布签名 ed25519 公钥 PEM；为空则 server 不强制验签
	//（agent 端始终强制），适合公钥尚未分发的首装阶段。
	ReleaseSigningPubkeyFile string `yaml:"release_signing_pubkey_file"`

	// HeartbeatTimeout 多久收不到心跳判 offline。
	HeartbeatTimeout Duration `yaml:"heartbeat_timeout"`
	// QueueTTL 离线 job（含升级）排队过期时间。
	QueueTTL Duration `yaml:"queue_ttl"`
	// AgentCertValidity 签发 agent 证书的有效期。
	AgentCertValidity Duration `yaml:"agent_cert_validity"`
	// EnrollmentTokenTTL 新建 token 的默认有效期。
	EnrollmentTokenTTL Duration `yaml:"enrollment_token_ttl"`
}

// DefaultServerConfig 返回带全部默认值的 server 配置。
func DefaultServerConfig() *ServerConfig {
	return &ServerConfig{
		EnrollmentListen:   defaultEnrollmentListen,
		ControlListen:      defaultControlListen,
		AdminListen:        defaultAdminListen,
		DataDir:            defaultServerDataDir,
		HeartbeatTimeout:   Duration(defaultHeartbeatTimeout),
		QueueTTL:           Duration(defaultQueueTTL),
		AgentCertValidity:  Duration(defaultAgentCertValidity),
		EnrollmentTokenTTL: Duration(defaultEnrollmentTokenTTL),
		ServerCertHosts:    append([]string(nil), defaultServerCertHosts...),
	}
}

// LoadServerConfig 从 path 读取 YAML：先置默认值再覆盖，未知字段直接报错。
func LoadServerConfig(path string) (*ServerConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取 server 配置 %s: %w", path, err)
	}

	cfg := DefaultServerConfig()
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(cfg); err != nil {
		return nil, fmt.Errorf("解析 server 配置 %s: %w", path, err)
	}

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("server 配置校验失败: %w", err)
	}
	return cfg, nil
}

// Validate 检查静态约束。
func (c *ServerConfig) Validate() error {
	if c.EnrollmentListen == "" {
		return errors.New("enrollment_listen 不能为空")
	}
	if c.ControlListen == "" {
		return errors.New("control_listen 不能为空")
	}
	if c.AdminListen == "" {
		return errors.New("admin_listen 不能为空")
	}
	if c.EnrollmentListen == c.ControlListen || c.EnrollmentListen == c.AdminListen ||
		c.ControlListen == c.AdminListen {
		return errors.New("enrollment/control/admin 三个监听地址必须互不相同")
	}
	if c.DataDir == "" {
		return errors.New("data_dir 不能为空")
	}
	if len(c.ServerCertHosts) == 0 {
		c.ServerCertHosts = append([]string(nil), defaultServerCertHosts...)
	}
	for _, host := range c.ServerCertHosts {
		if host == "" {
			return errors.New("server_cert_hosts 不能含空条目")
		}
	}
	if c.HeartbeatTimeout.Std() <= 0 {
		return errors.New("heartbeat_timeout 必须为正数")
	}
	if c.QueueTTL.Std() <= 0 {
		return errors.New("queue_ttl 必须为正数")
	}
	if c.AgentCertValidity.Std() <= 0 {
		return errors.New("agent_cert_validity 必须为正数")
	}
	if c.EnrollmentTokenTTL.Std() <= 0 {
		return errors.New("enrollment_token_ttl 必须为正数")
	}
	return nil
}

// DefaultServerConfigPath 返回标准安装路径。
func DefaultServerConfigPath() string {
	return defaultServerConfigPath
}
