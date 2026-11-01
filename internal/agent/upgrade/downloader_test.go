package upgrade

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/crazychengmm/cagent/internal/release"
	pb "github.com/crazychengmm/cagent/proto/cagent/v1"
)

// fakeDownloadClient 只实现 DownloadRelease，其余 client 方法走 nil 接口（测试不调）。
type fakeDownloadClient struct {
	pb.ControlServiceClient
	stream pb.ControlService_DownloadReleaseClient
	err    error
}

func (c *fakeDownloadClient) DownloadRelease(_ context.Context,
	_ *pb.DownloadReleaseRequest, _ ...grpc.CallOption) (pb.ControlService_DownloadReleaseClient, error) {
	return c.stream, c.err
}

type fakeDownloadStream struct {
	pb.ControlService_DownloadReleaseClient
	chunks [][]byte
	idx    int
	err    error
}

func (s *fakeDownloadStream) Recv() (*pb.ReleaseChunk, error) {
	if s.idx < len(s.chunks) {
		c := s.chunks[s.idx]
		s.idx++
		return &pb.ReleaseChunk{Data: c}, nil
	}
	if s.err != nil {
		return nil, s.err
	}
	return nil, io.EOF
}

func signedExpected(t *testing.T, pkg []byte, priv ed25519.PrivateKey) Expected {
	t.Helper()
	sum := sha256.Sum256(pkg)
	return Expected{
		Version:   "9.9.9",
		SHA256Hex: hex.EncodeToString(sum[:]),
		Size:      int64(len(pkg)),
		Signature: ed25519.Sign(priv, pkg),
	}
}

func chunkify(pkg []byte, n int) [][]byte {
	var chunks [][]byte
	for i := 0; i < len(pkg); i += n {
		end := i + n
		if end > len(pkg) {
			end = len(pkg)
		}
		chunks = append(chunks, pkg[i:end])
	}
	return chunks
}

func TestDownloadVerify_Success(t *testing.T) {
	t.Parallel()

	priv, pub, _ := release.GenerateSigningKey()
	pkg := []byte("the-release-payload-0123456789")
	expected := signedExpected(t, pkg, priv)
	client := &fakeDownloadClient{stream: &fakeDownloadStream{chunks: chunkify(pkg, 7)}}

	got, err := DownloadVerify(context.Background(), client, expected, pub)
	if err != nil {
		t.Fatalf("DownloadVerify: %v", err)
	}
	if string(got) != string(pkg) {
		t.Errorf("内容不一致: %q", got)
	}
}

func TestDownloadVerify_SizeMismatch(t *testing.T) {
	t.Parallel()

	priv, pub, _ := release.GenerateSigningKey()
	pkg := []byte("payload")
	expected := signedExpected(t, pkg, priv)
	expected.Size-- // 声明少 1 字节，实际收到更多
	client := &fakeDownloadClient{stream: &fakeDownloadStream{chunks: chunkify(pkg, 4)}}

	if _, err := DownloadVerify(context.Background(), client, expected, pub); !errors.Is(err, ErrSizeMismatch) {
		t.Errorf("应 ErrSizeMismatch，实际 %v", err)
	}
}

func TestDownloadVerify_ChecksumMismatch(t *testing.T) {
	t.Parallel()

	priv, pub, _ := release.GenerateSigningKey()
	pkg := []byte("payload")
	expected := signedExpected(t, pkg, priv)
	tampered := append([]byte(nil), pkg...)
	tampered[0] = 'x' // 大小一致但内容变了
	client := &fakeDownloadClient{stream: &fakeDownloadStream{chunks: chunkify(tampered, 4)}}

	if _, err := DownloadVerify(context.Background(), client, expected, pub); !errors.Is(err, ErrChecksumMismatch) {
		t.Errorf("应 ErrChecksumMismatch，实际 %v", err)
	}
}

func TestDownloadVerify_BadSignature(t *testing.T) {
	t.Parallel()

	priv, _, _ := release.GenerateSigningKey()     // 签名者
	_, otherPub, _ := release.GenerateSigningKey() // agent 只信任另一把公钥
	pkg := []byte("payload")
	expected := signedExpected(t, pkg, priv)
	client := &fakeDownloadClient{stream: &fakeDownloadStream{chunks: chunkify(pkg, 4)}}

	if _, err := DownloadVerify(context.Background(), client, expected, otherPub); !errors.Is(err, ErrBadSignature) {
		t.Errorf("应 ErrBadSignature，实际 %v", err)
	}
}

func TestDownloadVerify_RejectsInvalidPublicKey(t *testing.T) {
	t.Parallel()

	client := &fakeDownloadClient{}
	if _, err := DownloadVerify(context.Background(), client, Expected{}, []byte("short")); err == nil {
		t.Fatal("无效公钥应拒绝")
	}
}

func TestDownloadVerify_StreamError(t *testing.T) {
	t.Parallel()

	_, pub, _ := release.GenerateSigningKey()
	streamErr := status.Error(codes.Unavailable, "connection lost")
	client := &fakeDownloadClient{
		stream: &fakeDownloadStream{err: streamErr},
	}
	_, err := DownloadVerify(context.Background(), client, Expected{}, pub)
	if status.Code(err) != codes.Unavailable {
		t.Errorf("流错误应透传，实际 %v", err)
	}
}
