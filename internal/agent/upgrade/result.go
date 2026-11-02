package upgrade

import (
	"encoding/json"
	"os"
	"path/filepath"

	pb "github.com/crazychengmm/cagent/proto/cagent/v1"
)

const resultFileName = "upgrade-result.json"

// PendingResult 是 guard 写盘、由重启后的 agent 主连接代为上报的升级结果。
// 这样避免 guard 短连接与 agent 重连互相挤掉控制流导致事件丢失。
type PendingResult struct {
	JobID       string `json:"job_id"`
	FromVersion string `json:"from_version"`
	ToVersion   string `json:"to_version"`
	// succeeded / rolled_back / failed
	Status  string `json:"status"`
	Message string `json:"message"`
}

// ResultPath 返回结果文件路径。
func ResultPath(dataDir string) string {
	return filepath.Join(dataDir, resultFileName)
}

// SaveResult 原子写入待上报结果。
func SaveResult(dataDir string, r *PendingResult) error {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	path := ResultPath(dataDir)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// LoadResult 读取待上报结果；不存在返回 (nil, nil)。
func LoadResult(dataDir string) (*PendingResult, error) {
	data, err := os.ReadFile(ResultPath(dataDir))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var r PendingResult
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

// ClearResult 删除待上报结果。
func ClearResult(dataDir string) error {
	if err := os.Remove(ResultPath(dataDir)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// ToEvent 转为线上报事件。
func (r *PendingResult) ToEvent() *pb.UpgradeEvent {
	status := pb.UpgradeEvent_STATUS_FAILED
	switch r.Status {
	case "succeeded":
		status = pb.UpgradeEvent_STATUS_SUCCEEDED
	case "rolled_back":
		status = pb.UpgradeEvent_STATUS_ROLLED_BACK
	}
	return &pb.UpgradeEvent{
		JobId:       r.JobID,
		Status:      status,
		Message:     r.Message,
		FromVersion: r.FromVersion,
		ToVersion:   r.ToVersion,
	}
}
