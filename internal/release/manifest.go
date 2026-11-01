// Package release 实现升级包的打包、签名、发布与 agent 侧验签升级。
package release

// Manifest 是升级包内的清单文件。
type Manifest struct {
	// Version 是语义化版本号。
	Version string `json:"version"`
	// File 是包内二进制文件名。
	File string `json:"file"`
	// SHA256Hex 是二进制内容的 SHA-256 十六进制。
	SHA256Hex string `json:"sha256_hex"`
	// Size 是二进制字节数。
	Size int64 `json:"size"`
}

// ManifestName 是清单在 tar 包内的固定文件名。
const ManifestName = "manifest.json"

// BinaryName 是包内（也是安装后的）二进制名。
const BinaryName = "cagent"
