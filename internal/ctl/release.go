package ctl

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/crazychengmm/cagent/internal/release"
	pb "github.com/crazychengmm/cagent/proto/cagent/v1"
)

func runReleaseCmd(configPath string, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "用法: release keygen|package|sign|publish|list")
		return 2
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "keygen":
		return releaseKeygen(rest, stdout, stderr)
	case "package":
		return releasePackage(rest, stdout, stderr)
	case "sign":
		return releaseSign(rest, stdout, stderr)
	case "publish":
		return releasePublish(configPath, rest, stdout, stderr)
	case "list":
		return releaseList(configPath, stdout, stderr)
	default:
		fmt.Fprintf(stderr, "未知 release 子命令 %q\n", sub)
		return 2
	}
}

// release keygen --key <priv.pem> --pub <pub.pem>
// 生成 ed25519 发布签名密钥对（首次发布前离线执行，私钥务必妥善保管）。
func releaseKeygen(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("release keygen", flag.ContinueOnError)
	fs.SetOutput(stderr)
	keyPath := fs.String("key", "release.key", "私钥输出路径（PEM，0600）")
	pubPath := fs.String("pub", "release.pub", "公钥输出路径（PEM，随 agent 安装分发）")
	force := fs.Bool("force", false, "覆盖已存在的密钥文件")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	for _, p := range []string{*keyPath, *pubPath} {
		if _, err := os.Stat(p); err == nil && !*force {
			fmt.Fprintf(stderr, "%s 已存在（--force 覆盖）\n", p)
			return 1
		}
	}
	priv, pub, err := release.GenerateSigningKey()
	if err != nil {
		fmt.Fprintf(stderr, "生成密钥失败: %v\n", err)
		return 1
	}
	privPEM, err := release.EncodePrivateKeyPEM(priv)
	if err != nil {
		fmt.Fprintf(stderr, "编码私钥失败: %v\n", err)
		return 1
	}
	pubPEM, err := release.EncodePublicKeyPEM(pub)
	if err != nil {
		fmt.Fprintf(stderr, "编码公钥失败: %v\n", err)
		return 1
	}
	if err := os.WriteFile(*keyPath, privPEM, 0o600); err != nil {
		fmt.Fprintf(stderr, "写私钥失败: %v\n", err)
		return 1
	}
	if err := os.WriteFile(*pubPath, pubPEM, 0o644); err != nil {
		fmt.Fprintf(stderr, "写公钥失败: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "已生成签名密钥:\n  私钥(妥善保管): %s\n  公钥(分发给 agent/server): %s\n",
		*keyPath, *pubPath)
	return 0
}

// release package --binary cagent --version 1.2.3 [--out pkg.tar.gz]
func releasePackage(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("release package", flag.ContinueOnError)
	fs.SetOutput(stderr)
	binary := fs.String("binary", "", "待打包的二进制路径")
	version := fs.String("version", "", "语义版本 x.y.z")
	out := fs.String("out", "", "输出包路径（默认 cagent-<version>.pkg）")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *binary == "" || *version == "" {
		fmt.Fprintln(stderr, "用法: release package --binary <path> --version <x.y.z>")
		return 2
	}
	if _, err := release.Parse(*version); err != nil {
		fmt.Fprintf(stderr, "版本非法: %v\n", err)
		return 2
	}
	outPath := *out
	if outPath == "" {
		outPath = "cagent-" + *version + ".pkg"
	}
	f, err := os.Create(outPath)
	if err != nil {
		fmt.Fprintf(stderr, "创建输出失败: %v\n", err)
		return 1
	}
	defer f.Close()
	manifest, err := release.BuildPackage(f, *binary, *version)
	if err != nil {
		fmt.Fprintf(stderr, "打包失败: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "已生成 %s（sha256=%s size=%d）\n",
		outPath, manifest.SHA256Hex, manifest.Size)
	return 0
}

// release sign --key priv.pem --pkg file.pkg [--sig file.sig]
func releaseSign(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("release sign", flag.ContinueOnError)
	fs.SetOutput(stderr)
	keyPath := fs.String("key", "", "ed25519 私钥 PEM")
	pkgPath := fs.String("pkg", "", "待签名的包文件")
	sigPath := fs.String("sig", "", "签名输出路径（默认 <pkg>.sig）")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *keyPath == "" || *pkgPath == "" {
		fmt.Fprintln(stderr, "用法: release sign --key <priv.pem> --pkg <file.pkg>")
		return 2
	}
	keyPEM, err := os.ReadFile(*keyPath)
	if err != nil {
		fmt.Fprintf(stderr, "读取私钥失败: %v\n", err)
		return 1
	}
	priv, err := release.ParsePrivateKeyPEM(keyPEM)
	if err != nil {
		fmt.Fprintf(stderr, "解析私钥失败: %v\n", err)
		return 1
	}
	pkgFile, err := os.Open(*pkgPath)
	if err != nil {
		fmt.Fprintf(stderr, "打开包失败: %v\n", err)
		return 1
	}
	defer pkgFile.Close()
	signature, err := release.SignPackage(priv, pkgFile)
	if err != nil {
		fmt.Fprintf(stderr, "签名失败: %v\n", err)
		return 1
	}
	outPath := *sigPath
	if outPath == "" {
		outPath = *pkgPath + ".sig"
	}
	if err := os.WriteFile(outPath, signature, 0o600); err != nil {
		fmt.Fprintf(stderr, "写签名失败: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "已签名: %s\n", outPath)
	return 0
}

// release publish --pkg file.pkg --sig file.sig
func releasePublish(configPath string, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("release publish", flag.ContinueOnError)
	fs.SetOutput(stderr)
	pkgPath := fs.String("pkg", "", "升级包文件")
	sigPath := fs.String("sig", "", "签名文件")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *pkgPath == "" || *sigPath == "" {
		fmt.Fprintln(stderr, "用法: release publish --pkg <file.pkg> --sig <file.sig>")
		return 2
	}
	pkg, err := os.ReadFile(*pkgPath)
	if err != nil {
		fmt.Fprintf(stderr, "读取包失败: %v\n", err)
		return 1
	}
	signature, err := os.ReadFile(*sigPath)
	if err != nil {
		fmt.Fprintf(stderr, "读取签名失败: %v\n", err)
		return 1
	}
	manifest, err := readManifest(pkg)
	if err != nil {
		fmt.Fprintf(stderr, "读取包内清单失败: %v\n", err)
		return 1
	}
	// sha256 针对整个 .pkg（agent 下载后校验整包；签名同样覆盖整包）。
	hasher := sha256.New()
	hasher.Write(pkg)
	packageSHA := hex.EncodeToString(hasher.Sum(nil))

	ctx, cancel := context.WithTimeout(context.Background(), rpcTimeout)
	defer cancel()
	conn, err := dial(configPath, ctx, stderr)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer conn.Close()

	stream, err := pb.NewManagementServiceClient(conn).PublishRelease(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "建立发布流失败: %v\n", err)
		return 1
	}
	if err := stream.Send(&pb.PublishReleaseChunk{
		Chunk: &pb.PublishReleaseChunk_Meta{Meta: &pb.ReleaseMeta{
			Version:   manifest.Version,
			Sha256Hex: packageSHA,
			Size:      int64(len(pkg)),
			Signature: signature,
		}},
	}); err != nil {
		fmt.Fprintf(stderr, "发送 meta 失败: %v\n", err)
		return 1
	}
	chunkSize := 64 * 1024
	for off := 0; off < len(pkg); off += chunkSize {
		end := off + chunkSize
		if end > len(pkg) {
			end = len(pkg)
		}
		if err := stream.Send(&pb.PublishReleaseChunk{
			Chunk: &pb.PublishReleaseChunk_Data{Data: pkg[off:end]},
		}); err != nil {
			fmt.Fprintf(stderr, "发送数据失败: %v\n", err)
			return 1
		}
	}
	resp, err := stream.CloseAndRecv()
	if err != nil {
		fmt.Fprintf(stderr, "发布失败: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "已发布版本 %s\n", resp.Version)
	return 0
}

func releaseList(configPath string, stdout, stderr io.Writer) int {
	ctx, cancel := context.WithTimeout(context.Background(), rpcTimeout)
	defer cancel()
	conn, err := dial(configPath, ctx, stderr)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer conn.Close()
	resp, err := pb.NewManagementServiceClient(conn).ListReleases(ctx, &pb.ListReleasesRequest{})
	if err != nil {
		fmt.Fprintf(stderr, "查询失败: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "%-12s %-12s %-70s %s\n", "版本", "大小", "SHA256", "上传者")
	for _, r := range resp.Releases {
		fmt.Fprintf(stdout, "%-12s %-12d %-70s %s\n", r.Version, r.Size, r.Sha256Hex, r.UploadedBy)
	}
	fmt.Fprintf(stdout, "共 %d 个版本\n", len(resp.Releases))
	return 0
}

// readManifest 从 tar.gz 包中解析 manifest.json。
func readManifest(pkg []byte) (*release.Manifest, error) {
	gz, err := gzip.NewReader(bytes.NewReader(pkg))
	if err != nil {
		return nil, err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil, fmt.Errorf("包中缺少 manifest.json")
		}
		if err != nil {
			return nil, err
		}
		if hdr.Name == release.ManifestName {
			var m release.Manifest
			if err := json.NewDecoder(tr).Decode(&m); err != nil {
				return nil, err
			}
			return &m, nil
		}
	}
}
