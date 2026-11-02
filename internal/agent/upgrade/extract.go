package upgrade

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"

	"github.com/crazychengmm/cagent/internal/release"
)

// ExtractBinary 从 tar.gz 发布包中提取 cagent 二进制。
func ExtractBinary(pkg []byte) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(pkg))
	if err != nil {
		return nil, fmt.Errorf("gzip: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if hdr.Name == release.BinaryName {
			return io.ReadAll(tr)
		}
	}
	return nil, fmt.Errorf("包中缺少 %s", release.BinaryName)
}
