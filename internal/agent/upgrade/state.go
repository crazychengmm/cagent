package upgrade

import (
	"encoding/json"
	"os"
	"path/filepath"
)

const (
	stateFileName   = "upgrade-state.json"
	healthyFileName = "upgrade-healthy"
	stagedFileName  = "cagent.staged"
	backupFileName  = "cagent.old"
)

// UpgradeState 是一次升级的现场记录，主进程与独立 guard 进程共享。
type UpgradeState struct {
	JobID       string `json:"job_id"`
	FromVersion string `json:"from_version"`
	ToVersion   string `json:"to_version"`
	BinaryPath  string `json:"binary_path"`
	BackupPath  string `json:"backup_path"`
	StagedPath  string `json:"staged_path"`
	Unit        string `json:"unit"`
}

// StatePath 返回升级状态文件路径。
func StatePath(dataDir string) string {
	return filepath.Join(dataDir, stateFileName)
}

// SaveState 原子写入状态（0600）。
func SaveState(path string, st *UpgradeState) error {
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// LoadState 读取升级状态。
func LoadState(path string) (*UpgradeState, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var st UpgradeState
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, err
	}
	return &st, nil
}

// ClearState 删除状态文件（升级终态清理）。
func ClearState(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// MarkHealthy 由新版本 agent 在首次心跳成功后写入，供 guard 判定健康。
func MarkHealthy(dataDir, version string) error {
	return os.WriteFile(filepath.Join(dataDir, healthyFileName), []byte(version), 0o600)
}

// IsHealthy 判断健康标记是否存在且版本匹配。
func IsHealthy(dataDir, expectedVersion string) bool {
	data, err := os.ReadFile(filepath.Join(dataDir, healthyFileName))
	if err != nil {
		return false
	}
	return string(data) == expectedVersion
}

// ClearHealthy 删除健康标记。
func ClearHealthy(dataDir string) {
	_ = os.Remove(filepath.Join(dataDir, healthyFileName))
}
