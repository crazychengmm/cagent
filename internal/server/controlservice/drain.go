package controlservice

import (
	"context"

	"github.com/crazychengmm/cagent/internal/server/store"
	pb "github.com/crazychengmm/cagent/proto/cagent/v1"
)

// maxDrainPerReconnect 单次重连补投的任务上限；
// agent 的 running 去重与终态缓存保证重发幂等。
const maxDrainPerReconnect = 1000

// drainPendingJobs 在控制流建立后按创建顺序补投该 agent 的全部 pending job。
func (s *Service) drainPendingJobs(ctx context.Context, session *streamSession) {
	jobs, err := s.store.ListJobs(ctx, store.ListJobsFilter{
		AgentID: session.agentID,
		Status:  store.JobStatusPending,
		Limit:   maxDrainPerReconnect,
	})
	if err != nil {
		s.logger.Error("查询待补投任务失败", "agent_id", session.agentID, "error", err.Error())
		return
	}
	if len(jobs) == 0 {
		return
	}

	// ListJobs 按创建时间倒序，补投需正序。
	for i := len(jobs) - 1; i >= 0; i-- {
		job := jobs[i]
		msg := messageForPendingJob(job)
		if err := session.send(msg); err != nil {
			// 流已断：任务保持 pending，下次重连继续补投。
			s.logger.Warn("补投中断，剩余任务等待下次重连",
				"agent_id", session.agentID, "sent", len(jobs)-1-i, "error", err.Error())
			return
		}
		s.logger.Info("补投离线期间任务", "agent_id", session.agentID, "job_id", job.JobID, "kind", job.Kind)
	}
}

// messageForPendingJob 按任务类型构造补投消息。
func messageForPendingJob(job store.Job) *pb.ServerMessage {
	if job.Kind == store.KindUpgrade {
		return &pb.ServerMessage{
			Payload: &pb.ServerMessage_UpgradeCommand{
				UpgradeCommand: &pb.UpgradeCommand{
					JobId:          job.JobID,
					Version:        job.ReleaseVersion,
					Sha256Hex:      job.SHA256Hex,
					Signature:      job.Signature,
					PackageSize:    job.PackageSize,
					AllowDowngrade: job.AllowDowngrade,
					RequestedBy:    job.RequestedBy,
				},
			},
		}
	}
	return &pb.ServerMessage{
		Payload: &pb.ServerMessage_JobDispatch{
			JobDispatch: &pb.JobDispatch{
				JobId:          job.JobID,
				Command:        job.Command,
				WorkingDir:     job.WorkingDir,
				ExtraEnv:       job.ExtraEnv,
				TimeoutSeconds: job.TimeoutSeconds,
				RequestedBy:    job.RequestedBy,
			},
		},
	}
}
