// Package identity 管理 agent 的本地持久身份：UUID、私钥、由 server CA
// 签发的证书。所有材料保存在数据目录（0700），私钥与身份文件 0600。
package identity

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const (
	dirPerm  os.FileMode = 0o700
	filePerm os.FileMode = 0o600

	idFile    = "identity"
	keyFile   = "agent.key"
	certFile  = "agent.crt"
	caFile    = "ca.crt"
	tmpSuffix = ".tmp"
)

// Identity 是 agent 一次注册后获得并持久化的全部身份材料。
type Identity struct {
	AgentID string
	Key     *ecdsa.PrivateKey
	CertPEM []byte
	CAPEM   []byte
}

// Exists 判断数据目录中是否已有完整身份（UUID+私钥+证书）。
func Exists(dataDir string) bool {
	for _, name := range []string{idFile, keyFile, certFile} {
		if _, err := os.Stat(filepath.Join(dataDir, name)); err != nil {
			return false
		}
	}
	return true
}

// GenerateKey 生成一把新的 P-256 私钥（永不上传，仅用于 CSR 与 mTLS）。
func GenerateKey() (*ecdsa.PrivateKey, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("生成 agent 私钥: %w", err)
	}
	return key, nil
}

// MakeCSR 用私钥生成自签 CSR；CN 放主机名仅供展示，server 签发时改为 UUID。
func MakeCSR(key *ecdsa.PrivateKey, hostname string) ([]byte, error) {
	tmpl := x509.CertificateRequest{Subject: pkix.Name{CommonName: hostname}}
	der, err := x509.CreateCertificateRequest(rand.Reader, &tmpl, key)
	if err != nil {
		return nil, fmt.Errorf("生成 CSR: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der}), nil
}

// Save 首次注册成功后原子落盘全部身份材料，并确保目录权限为 0700。
func Save(dataDir, agentID string, key *ecdsa.PrivateKey, certPEM, caPEM []byte) error {
	if agentID == "" || key == nil || len(certPEM) == 0 {
		return errors.New("身份材料不完整")
	}
	if err := os.MkdirAll(dataDir, dirPerm); err != nil {
		return fmt.Errorf("创建身份目录 %s: %w", dataDir, err)
	}

	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return fmt.Errorf("序列化私钥: %w", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	files := []struct {
		name string
		data []byte
	}{
		{idFile, []byte(agentID)},
		{keyFile, keyPEM},
		{certFile, certPEM},
	}
	if len(caPEM) > 0 {
		files = append(files, struct {
			name string
			data []byte
		}{caFile, caPEM})
	}
	for _, f := range files {
		if err := writeAtomic(filepath.Join(dataDir, f.name), f.data, filePerm); err != nil {
			return err
		}
	}
	return nil
}

// Load 读取并校验本地身份；任一材料缺失或损坏返回错误。
func Load(dataDir string) (*Identity, error) {
	agentID, err := os.ReadFile(filepath.Join(dataDir, idFile))
	if err != nil {
		return nil, fmt.Errorf("读取身份文件: %w", err)
	}
	id := string(agentID)
	if id == "" {
		return nil, errors.New("身份文件为空")
	}

	keyPEM, err := os.ReadFile(filepath.Join(dataDir, keyFile))
	if err != nil {
		return nil, fmt.Errorf("读取私钥: %w", err)
	}
	block, _ := pem.Decode(keyPEM)
	if block == nil {
		return nil, errors.New("私钥 PEM 解析失败")
	}
	key, err := x509.ParseECPrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("解析私钥: %w", err)
	}

	certPEM, err := os.ReadFile(filepath.Join(dataDir, certFile))
	if err != nil {
		return nil, fmt.Errorf("读取证书: %w", err)
	}
	if err := validateCertPEM(certPEM); err != nil {
		return nil, err
	}

	ident := &Identity{AgentID: id, Key: key, CertPEM: certPEM}
	if caPEM, err := os.ReadFile(filepath.Join(dataDir, caFile)); err == nil {
		ident.CAPEM = caPEM
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("读取 CA 证书: %w", err)
	}
	return ident, nil
}

// ReplaceCertificate 原子替换 agent 证书（续签成功后调用）。
func ReplaceCertificate(dataDir string, certPEM []byte) error {
	if err := validateCertPEM(certPEM); err != nil {
		return fmt.Errorf("拒绝写入非法证书: %w", err)
	}
	return writeAtomic(filepath.Join(dataDir, certFile), certPEM, filePerm)
}

func validateCertPEM(certPEM []byte) error {
	block, _ := pem.Decode(certPEM)
	if block == nil || block.Type != "CERTIFICATE" {
		return errors.New("证书 PEM 解析失败")
	}
	if _, err := x509.ParseCertificate(block.Bytes); err != nil {
		return fmt.Errorf("解析证书: %w", err)
	}
	return nil
}

// writeAtomic 先写临时文件再 rename，保证同目录下替换的原子性。
func writeAtomic(path string, data []byte, perm os.FileMode) error {
	tmp := path + tmpSuffix
	if err := os.WriteFile(tmp, data, perm); err != nil {
		return fmt.Errorf("写 %s: %w", path, err)
	}
	if err := os.Chmod(tmp, perm); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("设置 %s 权限: %w", path, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("替换 %s: %w", path, err)
	}
	return nil
}
