package management

import (
	"context"
	"encoding/json"
	"errors"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/google/uuid"

	"github.com/crazychengmm/cagent/internal/server/dispatch"
	"github.com/crazychengmm/cagent/internal/server/store"
	pb "github.com/crazychengmm/cagent/proto/cagent/v1"
)

// Upgrade 为每台匹配 agent 创建升级 job，在线机立即下发 UpgradeCommand，
// 离线机保持 pending 由控制流补投（复用 job 排队/幂等/过期机制）。
func (s *Service) Upgrade(ctx context.Context, req *pb.UpgradeRequest) (*pb.UpgradeResponse, error) {
	version := req.GetVersion()
	release, err := s.store.GetRelease(ctx, version)
	if errors.Is(err, store.ErrNotFound) {
		return nil, status.Errorf(codes.FailedPrecondition, "版本 %s 尚未发布", version)
	}
	if err != nil {
		return nil, status.Error(codes.Internal, "查询发布版本失败")
	}

	agents, err := s.store.ListAgents(ctx)
	if err != nil {
		return nil, status.Error(codes.Internal, "查询 agent 失败")
	}
	matched := make([]store.Agent, 0)
	for _, agent := range agents {
		if dispatch.MatchLabels(agent.Labels, req.GetSelector()) {
			matched = append(matched, agent)
		}
	}
	if len(matched) == 0 {
		return nil, status.Error(codes.FailedPrecondition, "选择器未匹配到任何 agent")
	}

	online := map[string]struct{}{}
	if s.online != nil {
		for _, id := range s.online.OnlineAgentIDs() {
			online[id] = struct{}{}
		}
	}

	now := s.now().UTC()
	jobIDs := make([]string, 0, len(matched))
	for _, agent := range matched {
		jobID := "job_" + uuid.New().String()
		if err := s.store.CreateJob(ctx, store.Job{
			JobID:          jobID,
			AgentID:        agent.AgentID,
			Kind:           store.KindUpgrade,
			Status:         store.JobStatusPending,
			RequestedBy:    actorFromContext(ctx),
			CreatedAt:      now,
			ReleaseVersion: release.Version,
			SHA256Hex:      release.SHA256Hex,
			Signature:      release.Signature,
			PackageSize:    release.Size,
			AllowDowngrade: req.GetAllowDowngrade(),
		}); err != nil {
			return nil, status.Errorf(codes.Internal, "创建升级 job 失败: %v", err)
		}
		jobIDs = append(jobIDs, jobID)

		if _, connected := online[agent.AgentID]; connected && s.online != nil {
			msg := &pb.ServerMessage{
				Payload: &pb.ServerMessage_UpgradeCommand{
					UpgradeCommand: &pb.UpgradeCommand{
						JobId:          jobID,
						Version:        release.Version,
						Sha256Hex:      release.SHA256Hex,
						Signature:      release.Signature,
						PackageSize:    release.Size,
						AllowDowngrade: req.GetAllowDowngrade(),
						RequestedBy:    actorFromContext(ctx),
					},
				},
			}
			if err := s.online.SendToAgent(agent.AgentID, msg); err != nil {
				s.logger.Warn("升级指令在线下发失败，等待补投",
					"agent_id", agent.AgentID, "version", version, "error", err.Error())
			}
		}
	}

	detail, _ := json.Marshal(map[string]any{
		"selector":        req.GetSelector(),
		"version":         version,
		"matched":         len(matched),
		"allow_downgrade": req.GetAllowDowngrade(),
		"job_ids":         jobIDs,
	})
	if err := s.store.InsertAudit(ctx, store.AuditLog{
		At:     now,
		Actor:  actorFromContext(ctx),
		Action: store.ActionUpgradeDispatch,
		Detail: detail,
	}); err != nil {
		return nil, status.Error(codes.Internal, "写审计失败")
	}

	s.logger.Info("升级任务已创建",
		"version", version, "matched", len(matched), "online", len(online))
	return &pb.UpgradeResponse{JobIds: jobIDs, Matched: int32(len(matched))}, nil
}
