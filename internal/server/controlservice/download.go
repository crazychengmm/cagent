package controlservice

import (
	"errors"
	"io"
	"os"
	"path/filepath"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/crazychengmm/cagent/internal/server/store"
	pb "github.com/crazychengmm/cagent/proto/cagent/v1"
)

// downloadChunkSize 是升级包流式下发的块大小。
const downloadChunkSize = 64 * 1024

// DownloadRelease 向已认证 agent 流式发送指定版本的发布包。
func (s *Service) DownloadRelease(req *pb.DownloadReleaseRequest,
	stream pb.ControlService_DownloadReleaseServer) error {
	if _, err := s.store.GetRelease(stream.Context(), req.GetVersion()); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return status.Errorf(codes.NotFound, "版本 %s 不存在", req.GetVersion())
		}
		return status.Error(codes.Internal, "查询发布版本失败")
	}

	path := filepath.Join(s.packageDir, req.GetVersion()+".pkg")
	f, err := os.Open(path)
	if err != nil {
		s.logger.Error("发布包文件缺失", "version", req.GetVersion(), "path", path)
		return status.Errorf(codes.NotFound, "版本 %s 的包文件缺失", req.GetVersion())
	}
	defer f.Close()

	buf := make([]byte, downloadChunkSize)
	for {
		n, readErr := f.Read(buf)
		if n > 0 {
			if sendErr := stream.Send(&pb.ReleaseChunk{Data: append([]byte(nil), buf[:n]...)}); sendErr != nil {
				return sendErr
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return status.Errorf(codes.Internal, "读发布包失败: %v", readErr)
		}
	}
	return nil
}
