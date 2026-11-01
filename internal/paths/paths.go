// Package paths 定义受管 Linux 主机上的标准安装布局常量。
// 生产安装由 scripts/install.sh 与 deploy/*.service 共同落地该布局。
package paths

const (
	// BinaryPath 是 agent/server 二进制安装位置（同一二进制按子命令分角色）。
	BinaryPath = "/usr/local/bin/cagent"
	// ConfigPath 是 agent 配置文件。
	ConfigPath = "/etc/cagent/config.yaml"
	// ServerConfigPath 是 server 配置文件。
	ServerConfigPath = "/etc/cagent/server.yaml"
	// ConfigDir 是配置目录。
	ConfigDir = "/etc/cagent"

	// DataDir 是 agent 状态根（仅 root 可读写，0700）。
	DataDir = "/var/lib/cagent"
	// IdentityFile UUID 文件。
	IdentityFile = "identity"
	// AgentKeyFile 私钥。
	AgentKeyFile = "agent.key"
	// AgentCertFile 身份证书。
	AgentCertFile = "agent.crt"
	// CACertFile 注册时保存的 server CA。
	CACertFile = "ca.crt"
	// AuditFile 本地执行审计 JSONL。
	AuditFile = "audit.log"
	// ReleasesDir 旧版本副本、暂存包、升级状态所在子目录。
	ReleasesDir = "releases"
	// StateFile 升级现场状态。
	StateFile = "upgrade-state.json"
	// HealthyFile 新版本健康标记。
	HealthyFile = "upgrade-healthy"

	// ServerDataDir 是 server 数据目录。
	ServerDataDir = "/var/lib/cagent-server"
	// ServerDBFile SQLite 数据库文件名。
	ServerDBFile = "cagent-server.db"
	// ServerReleasesDir 发布包存储子目录。
	ServerReleasesDir = "releases"

	// AgentUnit / ServerUnit 是 systemd 单元名。
	AgentUnit  = "cagent.service"
	ServerUnit = "cagent-server.service"

	// 默认服务端口。
	EnrollmentListen = ":9443"
	ControlListen    = ":9444"
	AdminListen      = ":9445"
)
