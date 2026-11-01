package release

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
)

// BuildPackage 读取 binaryPath 处的二进制，向 dst 流式写入 tar.gz 升级包：
//   - cagent（0755，可执行）
//   - manifest.json（0644，版本与二进制 SHA-256）
//
// 返回写入的清单。
func BuildPackage(dst io.Writer, binaryPath, version string) (*Manifest, error) {
	if version == "" {
		return nil, fmt.Errorf("版本号不能为空")
	}
	f, err := os.Open(binaryPath)
	if err != nil {
		return nil, fmt.Errorf("打开二进制 %s: %w", binaryPath, err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}

	hasher := sha256.New()
	if _, err := io.Copy(hasher, f); err != nil {
		return nil, fmt.Errorf("计算二进制摘要: %w", err)
	}
	manifest := &Manifest{
		Version:   version,
		File:      BinaryName,
		SHA256Hex: hex.EncodeToString(hasher.Sum(nil)),
		Size:      info.Size(),
	}
	manifestJSON, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return nil, err
	}

	gz := gzip.NewWriter(dst)
	tw := tar.NewWriter(gz)

	if err := tw.WriteHeader(&tar.Header{
		Name: BinaryName, Mode: 0o755, Size: info.Size(),
	}); err != nil {
		return nil, fmt.Errorf("写二进制头: %w", err)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	if _, err := io.Copy(tw, f); err != nil {
		return nil, fmt.Errorf("写二进制内容: %w", err)
	}

	if err := tw.WriteHeader(&tar.Header{
		Name: ManifestName, Mode: 0o644, Size: int64(len(manifestJSON)),
	}); err != nil {
		return nil, fmt.Errorf("写清单头: %w", err)
	}
	if _, err := tw.Write(manifestJSON); err != nil {
		return nil, fmt.Errorf("写清单内容: %w", err)
	}

	if err := tw.Close(); err != nil {
		return nil, fmt.Errorf("关闭 tar: %w", err)
	}
	if err := gz.Close(); err != nil {
		return nil, fmt.Errorf("关闭 gzip: %w", err)
	}
	return manifest, nil
}
