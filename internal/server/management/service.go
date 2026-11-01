// Package management 实现 cagent ctl 使用的管理面 gRPC（admin 端口，
// 经 token 拦截器鉴权）。本文件实现发布物上传与列表，其余 RPC 在后续任务补齐。
package management

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/crazychengmm/cagent/internal/release"
	"github.com/crazychengmm/cagent/internal/server/dispatch"
	"github.com/crazychengmm/cagent/internal/server/store"
	pb "github.com/crazychengmm/cagent/proto/cagent/v1"
)

// 包目录名与上传保护上限。
const (
	ReleaseDirName = "releases"
	maxPackageSize = 512 << 20 // 512MB
)

// OnlineDispatcher 是在线控制通道能力（由 controlservice.Service 满足）。
type OnlineDispatcher interface {
	OnlineAgentIDs() []string
	SendToAgent(agentID string, msg *pb.ServerMessage) error
}

// JobCreator 创建任务（由 dispatch.Service 满足）。
type JobCreator interface {
	CreateJobs(ctx context.Context, req dispatch.CreateRequest) (*dispatch.CreateResult, error)
}

// TokenService 管理 enrollment token。
type TokenService interface {
	CreateToken(ctx context.Context, description, createdBy string, ttl time.Duration) (string, *store.EnrollmentToken, error)
}

// Service 是 ManagementService 的实现。
type Service struct {
	pb.UnimplementedManagementServiceServer

	store      *store.Store
	packageDir string
	publicKey  ed25519.PublicKey // 可空：未配置时 server 不强制验签（由 agent 强制）
	online     OnlineDispatcher
	tokens     TokenService
	jobCreator JobCreator
	now        func() time.Time
	logger     *slog.Logger
}

// New 构造管理服务；包文件存 dataDir/releases。
func New(s *store.Store, dataDir string, publicKey ed25519.PublicKey,
	online OnlineDispatcher, tokens TokenService, jobs JobCreator,
	now func() time.Time, logger *slog.Logger) (*Service, error) {
	packageDir := filepath.Join(dataDir, ReleaseDirName)
	if err := os.MkdirAll(packageDir, 0o700); err != nil {
		return nil, err
	}
	if now == nil {
		now = time.Now
	}
	return &Service{
		store:      s,
		packageDir: packageDir,
		publicKey:  publicKey,
		online:     online,
		tokens:     tokens,
		jobCreator: jobs,
		now:        now,
		logger:     logger,
	}, nil
}

// PublishRelease 接收 meta + 数据流，校验哈希/签名后原子落盘并登记。
func (s *Service) PublishRelease(stream pb.ManagementService_PublishReleaseServer) error {
	var (
		meta    *pb.ReleaseMeta
		tmpPath string
		size    int64
	)

	tmp, err := os.CreateTemp(s.packageDir, ".upload-*")
	if err != nil {
		return status.Errorf(codes.Internal, "创建临时文件失败: %v", err)
	}
	tmpPath = tmp.Name()
	defer os.Remove(tmpPath)

	hasher := sha256.New()
	abort := func(code codes.Code, msg string) error {
		tmp.Close()
		return status.Error(code, msg)
	}

	for {
		chunk, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return abort(codes.Internal, "接收失败: "+err.Error())
		}
		switch c := chunk.Chunk.(type) {
		case *pb.PublishReleaseChunk_Meta:
			if meta != nil {
				return abort(codes.InvalidArgument, "meta 只能发送一次")
			}
			meta = c.Meta
			if err := validateMeta(meta); err != nil {
				return abort(codes.InvalidArgument, err.Error())
			}
		case *pb.PublishReleaseChunk_Data:
			if meta == nil {
				return abort(codes.InvalidArgument, "首帧必须是 meta")
			}
			data := c.Data
			if size+int64(len(data)) > meta.Size || size+int64(len(data)) > maxPackageSize {
				return abort(codes.InvalidArgument, "上传数据超过声明大小或上限")
			}
			if _, err := tmp.Write(data); err != nil {
				return abort(codes.Internal, "写临时文件失败: "+err.Error())
			}
			hasher.Write(data)
			size += int64(len(data))
		}
	}
	if err := tmp.Close(); err != nil {
		return status.Errorf(codes.Internal, "关闭临时文件失败: %v", err)
	}

	if meta == nil {
		return status.Error(codes.InvalidArgument, "缺少 meta")
	}
	if size != meta.Size {
		return status.Errorf(codes.InvalidArgument, "大小不符: 收到 %d 声明 %d", size, meta.Size)
	}
	if actual := hex.EncodeToString(hasher.Sum(nil)); actual != meta.Sha256Hex {
		return status.Errorf(codes.InvalidArgument, "SHA-256 不符: 收到 %s", actual)
	}

	// 查重（不覆盖已发布版本）。
	if _, err := s.store.GetRelease(stream.Context(), meta.Version); err == nil {
		return status.Errorf(codes.AlreadyExists, "版本 %s 已存在", meta.Version)
	}

	pkgData, err := os.ReadFile(tmpPath)
	if err != nil {
		return status.Errorf(codes.Internal, "回读包失败: %v", err)
	}
	if s.publicKey != nil && !release.VerifyPackage(s.publicKey, pkgData, meta.Signature) {
		return status.Error(codes.InvalidArgument, "ed25519 签名校验失败")
	}

	finalPath := filepath.Join(s.packageDir, meta.Version+".pkg")
	if err := os.Rename(tmpPath, finalPath); err != nil {
		return status.Errorf(codes.Internal, "落盘失败: %v", err)
	}
	tmpPath = "" // rename 成功，defer 的 Remove 目标已不存在

	now := s.now().UTC()
	if err := s.store.InsertRelease(stream.Context(), store.Release{
		Version:    meta.Version,
		Size:       size,
		SHA256Hex:  meta.Sha256Hex,
		Signature:  meta.Signature,
		UploadedBy: actorFromContext(stream.Context()),
		UploadedAt: now,
	}); err != nil {
		_ = os.Remove(finalPath)
		return status.Errorf(codes.Internal, "登记发布失败: %v", err)
	}

	s.logger.Info("发布新版本", "version", meta.Version, "size", size)
	return stream.SendAndClose(&pb.PublishReleaseResponse{Version: meta.Version})
}

// ListReleases 返回已发布版本（倒序）。
func (s *Service) ListReleases(ctx context.Context,
	_ *pb.ListReleasesRequest) (*pb.ListReleasesResponse, error) {
	releases, err := s.store.ListReleases(ctx)
	if err != nil {
		return nil, status.Error(codes.Internal, "查询发布列表失败")
	}
	infos := make([]*pb.ReleaseInfo, 0, len(releases))
	for _, r := range releases {
		infos = append(infos, &pb.ReleaseInfo{
			Version:    r.Version,
			Size:       r.Size,
			Sha256Hex:  r.SHA256Hex,
			UploadedBy: r.UploadedBy,
			UploadedAt: timestamppb.New(r.UploadedAt),
		})
	}
	return &pb.ListReleasesResponse{Releases: infos}, nil
}

func validateMeta(meta *pb.ReleaseMeta) error {
	if meta.GetVersion() == "" || strings.ContainsAny(meta.GetVersion(), " /\t\n") {
		return errors.New("version 非法")
	}
	if meta.GetSize() <= 0 {
		return errors.New("size 必须为正")
	}
	if len(meta.GetSha256Hex()) != sha256.Size*2 {
		return errors.New("sha256_hex 必须是 64 位十六进制")
	}
	if _, err := hex.DecodeString(meta.GetSha256Hex()); err != nil {
		return errors.New("sha256_hex 不是合法十六进制")
	}
	if len(meta.GetSignature()) != ed25519.SignatureSize {
		return errors.New("signature 必须是 64 字节 ed25519 签名")
	}
	return nil
}

// actorFromContext 预留：拦截器注入操作者后从 metadata 读取；当前单 token 固定 admin。
func actorFromContext(_ context.Context) string {
	return "admin"
}
