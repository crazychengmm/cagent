// Package upgrade 实现 agent 侧升级包的下载、验真、grace 与自升级。
package upgrade

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"

	pb "github.com/crazychengmm/cagent/proto/cagent/v1"
)

// 验真错误。
var (
	// ErrSizeMismatch 表示下载字节数与升级指令声明不一致。
	ErrSizeMismatch = errors.New("升级包大小不符")
	// ErrChecksumMismatch 表示 SHA-256 与声明不一致。
	ErrChecksumMismatch = errors.New("升级包 SHA-256 校验失败")
	// ErrBadSignature 表示 ed25519 签名验证失败。
	ErrBadSignature = errors.New("升级包签名校验失败")
)

// Expected 是随升级指令下发的防篡改预期值（本身经 mTLS 通道送达）。
type Expected struct {
	Version   string
	SHA256Hex string
	Size      int64
	Signature []byte
}

// DownloadVerify 流式下载发布包并依次校验大小、SHA-256、ed25519 签名；
// 任一失败即拒绝，绝不返回包内容。
func DownloadVerify(ctx context.Context, client pb.ControlServiceClient,
	expected Expected, publicKey ed25519.PublicKey) ([]byte, error) {
	if len(publicKey) != ed25519.PublicKeySize {
		return nil, errors.New("未配置有效的发布签名公钥")
	}
	stream, err := client.DownloadRelease(ctx, &pb.DownloadReleaseRequest{
		Version: expected.Version,
	})
	if err != nil {
		return nil, fmt.Errorf("发起下载失败: %w", err)
	}

	hasher := sha256.New()
	data := make([]byte, 0, expected.Size)
	var received int64
	for {
		chunk, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("接收发布包失败: %w", err)
		}
		data = append(data, chunk.GetData()...)
		hasher.Write(chunk.GetData())
		received += int64(len(chunk.GetData()))
		if expected.Size > 0 && received > expected.Size {
			return nil, fmt.Errorf("%w: 收到 %d 声明 %d", ErrSizeMismatch, received, expected.Size)
		}
	}

	if received != expected.Size {
		return nil, fmt.Errorf("%w: 收到 %d 声明 %d", ErrSizeMismatch, received, expected.Size)
	}
	sum := hex.EncodeToString(hasher.Sum(nil))
	if sum != expected.SHA256Hex {
		return nil, fmt.Errorf("%w: 收到 %s", ErrChecksumMismatch, sum)
	}
	if !ed25519.Verify(publicKey, data, expected.Signature) {
		return nil, ErrBadSignature
	}
	return data, nil
}
