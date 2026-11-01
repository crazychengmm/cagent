// Package admin 管理控制面（cagent ctl）的认证：首启生成 admin token 文件，
// 并提供 gRPC 拦截器对管理 API 做令牌校验。
package admin

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	adminTokenFile   = "admin_token"
	adminTokenPrefix = "cactl_"
	adminTokenBytes  = 32

	adminFilePerm os.FileMode = 0o600
	adminDirPerm  os.FileMode = 0o700
)

// EnsureAdminToken 返回数据目录中的 admin token；文件不存在时首启生成。
// 文件权限固定 0600。token 内容只存在该文件，日志中不得打印。
func EnsureAdminToken(dataDir string) (string, error) {
	path := filepath.Join(dataDir, adminTokenFile)

	if raw, err := os.ReadFile(path); err == nil {
		token := strings.TrimSpace(string(raw))
		if token == "" {
			return "", fmt.Errorf("%s 内容为空，请删除后重启重新生成", path)
		}
		return token, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("读取 admin token: %w", err)
	}

	buf := make([]byte, adminTokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("生成 admin token: %w", err)
	}
	token := adminTokenPrefix + base64.RawURLEncoding.EncodeToString(buf)

	if err := os.MkdirAll(dataDir, adminDirPerm); err != nil {
		return "", fmt.Errorf("创建数据目录 %s: %w", dataDir, err)
	}
	if err := os.WriteFile(path, []byte(token), adminFilePerm); err != nil {
		return "", fmt.Errorf("写 admin token: %w", err)
	}
	return token, nil
}
