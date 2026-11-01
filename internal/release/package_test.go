package release

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestBuildPackage_ContentsAndManifest(t *testing.T) {
	t.Parallel()

	binary := []byte("#!/bin/sh\necho fake-binary\n")
	dir := t.TempDir()
	binPath := filepath.Join(dir, "cagent")
	if err := os.WriteFile(binPath, binary, 0o755); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	manifest, err := BuildPackage(&buf, binPath, "1.2.3")
	if err != nil {
		t.Fatalf("BuildPackage: %v", err)
	}

	sum := sha256.Sum256(binary)
	if manifest.SHA256Hex != hex.EncodeToString(sum[:]) ||
		manifest.Version != "1.2.3" || manifest.Size != int64(len(binary)) ||
		manifest.File != BinaryName {
		t.Errorf("manifest 不符: %+v", manifest)
	}

	gz, err := gzip.NewReader(&buf)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	files := map[string][]byte{}
	modes := map[string]int64{}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		content, err := io.ReadAll(tr)
		if err != nil {
			t.Fatal(err)
		}
		files[hdr.Name] = content
		modes[hdr.Name] = hdr.Mode
	}

	bin, ok := files[BinaryName]
	if !ok || !bytes.Equal(bin, binary) {
		t.Error("包内二进制内容不符")
	}
	if modes[BinaryName] != 0o755 {
		t.Errorf("二进制权限 = %o", modes[BinaryName])
	}

	rawManifest, ok := files[ManifestName]
	if !ok {
		t.Fatal("缺少 manifest.json")
	}
	var unpacked Manifest
	if err := json.Unmarshal(rawManifest, &unpacked); err != nil {
		t.Fatal(err)
	}
	if unpacked.SHA256Hex != manifest.SHA256Hex {
		t.Error("包内 manifest 与返回值不一致")
	}
}

func TestBuildPackage_RejectsEmptyVersion(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "cagent")
	if err := os.WriteFile(path, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := BuildPackage(io.Discard, path, ""); err == nil {
		t.Fatal("空版本应报错")
	}
}
