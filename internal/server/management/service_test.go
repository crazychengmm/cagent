package management

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/crazychengmm/cagent/internal/release"
	"github.com/crazychengmm/cagent/internal/server/store"
	pb "github.com/crazychengmm/cagent/proto/cagent/v1"
)

type fakePublishStream struct {
	grpc.ServerStream
	ctx    context.Context
	chunks []*pb.PublishReleaseChunk
	index  int
	resp   *pb.PublishReleaseResponse
}

func (f *fakePublishStream) Context() context.Context { return f.ctx }
func (f *fakePublishStream) SendAndClose(r *pb.PublishReleaseResponse) error {
	f.resp = r
	return nil
}
func (f *fakePublishStream) Recv() (*pb.PublishReleaseChunk, error) {
	if f.index >= len(f.chunks) {
		return nil, io.EOF
	}
	c := f.chunks[f.index]
	f.index++
	return c, nil
}

func newManagement(t *testing.T, publicKey ed25519.PublicKey) (*Service, *store.Store, string) {
	t.Helper()
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	dataDir := t.TempDir()
	svc, err := New(db, dataDir, publicKey, time.Now,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return svc, db, dataDir
}

func signedUpload(t *testing.T, pkg []byte, version string,
	priv ed25519.PrivateKey) []*pb.PublishReleaseChunk {
	t.Helper()
	sum := sha256.Sum256(pkg)
	sig := ed25519.Sign(priv, pkg)
	meta := &pb.ReleaseMeta{
		Version:   version,
		Sha256Hex: hex.EncodeToString(sum[:]),
		Size:      int64(len(pkg)),
		Signature: sig,
	}
	half := len(pkg) / 2
	return []*pb.PublishReleaseChunk{
		{Chunk: &pb.PublishReleaseChunk_Meta{Meta: meta}},
		{Chunk: &pb.PublishReleaseChunk_Data{Data: pkg[:half]}},
		{Chunk: &pb.PublishReleaseChunk_Data{Data: pkg[half:]}},
	}
}

func TestPublishRelease_SuccessAndPersisted(t *testing.T) {
	t.Parallel()

	priv, pub, _ := release.GenerateSigningKey()
	svc, db, dataDir := newManagement(t, pub)
	pkg := []byte("fake-package-content")
	stream := &fakePublishStream{
		ctx:    context.Background(),
		chunks: signedUpload(t, pkg, "1.0.0", priv),
	}
	if err := svc.PublishRelease(stream); err != nil {
		t.Fatalf("PublishRelease: %v", err)
	}
	if stream.resp.GetVersion() != "1.0.0" {
		t.Errorf("response = %+v", stream.resp)
	}

	r, err := db.GetRelease(context.Background(), "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if r.Size != int64(len(pkg)) {
		t.Errorf("登记 size = %d", r.Size)
	}
	content, err := os.ReadFile(filepath.Join(dataDir, releaseDirName, "1.0.0.pkg"))
	if err != nil {
		t.Fatalf("包文件未落盘: %v", err)
	}
	if string(content) != string(pkg) {
		t.Error("落盘内容不一致")
	}

	// upload-* 临时文件应已清理。
	entries, _ := os.ReadDir(filepath.Join(dataDir, releaseDirName))
	for _, e := range entries {
		if e.Name() != "1.0.0.pkg" {
			t.Errorf("存在残留文件: %s", e.Name())
		}
	}
}

func TestPublishRelease_BadSignatureRejected(t *testing.T) {
	t.Parallel()

	_, pub, _ := release.GenerateSigningKey() // server 只信任 pub
	otherPub, otherPriv, _ := ed25519.GenerateKey(nil)
	_ = otherPub
	svc, _, _ := newManagement(t, pub)

	pkg := []byte("pkg")
	stream := &fakePublishStream{
		ctx:    context.Background(),
		chunks: signedUpload(t, pkg, "1.0.0", otherPriv),
	}
	err := svc.PublishRelease(stream)
	if status.Code(err) != codes.InvalidArgument {
		t.Errorf("错误签名应 InvalidArgument，实际 %v", err)
	}
}

func TestPublishRelease_HashAndSizeMismatch(t *testing.T) {
	t.Parallel()

	priv, pub, _ := release.GenerateSigningKey()
	svc, _, _ := newManagement(t, pub)
	pkg := []byte("pkg")
	chunks := signedUpload(t, pkg, "1.0.0", priv)
	chunks[1].Chunk = &pb.PublishReleaseChunk_Data{Data: []byte("tampered")}
	// 声明 size 与数据不符：篡改 meta size
	chunks[0].GetMeta().Size = 99
	err := svc.PublishRelease(&fakePublishStream{ctx: context.Background(), chunks: chunks})
	if status.Code(err) != codes.InvalidArgument {
		t.Errorf("size 不符应 InvalidArgument，实际 %v", err)
	}
}

func TestPublishRelease_FirstChunkMustBeMeta(t *testing.T) {
	t.Parallel()

	priv, pub, _ := release.GenerateSigningKey()
	svc, _, _ := newManagement(t, pub)
	chunks := signedUpload(t, []byte("pkg"), "1.0.0", priv)
	chunks[0] = &pb.PublishReleaseChunk{Chunk: &pb.PublishReleaseChunk_Data{Data: []byte("x")}}
	err := svc.PublishRelease(&fakePublishStream{ctx: context.Background(), chunks: chunks})
	if status.Code(err) != codes.InvalidArgument {
		t.Errorf("首帧 data 应 InvalidArgument，实际 %v", err)
	}
}

func TestPublishRelease_DuplicateVersion(t *testing.T) {
	t.Parallel()

	priv, pub, _ := release.GenerateSigningKey()
	svc, _, _ := newManagement(t, pub)
	pkg := []byte("pkg")
	upload := func() error {
		return svc.PublishRelease(&fakePublishStream{
			ctx:    context.Background(),
			chunks: signedUpload(t, pkg, "2.0.0", priv),
		})
	}
	if err := upload(); err != nil {
		t.Fatal(err)
	}
	if err := upload(); status.Code(err) != codes.AlreadyExists {
		t.Errorf("重复版本应 AlreadyExists，实际 %v", err)
	}
}

func TestListReleases(t *testing.T) {
	t.Parallel()

	priv, pub, _ := release.GenerateSigningKey()
	svc, _, _ := newManagement(t, pub)
	pkg := []byte("pkg")
	for _, v := range []string{"1.0.0", "1.1.0"} {
		if err := svc.PublishRelease(&fakePublishStream{
			ctx:    context.Background(),
			chunks: signedUpload(t, pkg, v, priv),
		}); err != nil {
			t.Fatal(err)
		}
	}
	resp, err := svc.ListReleases(context.Background(), &pb.ListReleasesRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Releases) != 2 {
		t.Fatalf("版本数 = %d", len(resp.Releases))
	}
}
