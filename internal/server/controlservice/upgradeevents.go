package controlservice

import (
	"context"
	"encoding/json"
	"time"

	"github.com/crazychengmm/cagent/internal/server/store"
	pb "github.com/crazychengmm/cagent/proto/cagent/v1"
)

// HandleUpgradeEvent 处理 agent 上报的升级状态，推进 upgrade job 的状态机。
func (s *Service) HandleUpgradeEvent(ctx context.Context, ev *pb.UpgradeEvent) error {
	jobID := ev.GetJobId()
	job, err := s.store.GetJob(ctx, jobID)
	if err != nil {
		if err == store.ErrNotFound {
			s.logger.Warn("收到未知 upgrade job 事件，忽略", "job_id", jobID)
			return nil
		}
		return err
	}

	now := s.now().UTC()
	switch ev.GetStatus() {
	case pb.UpgradeEvent_STATUS_INSTALLING:
		moved, err := s.store.UpdateJobState(ctx, jobID, store.JobStatusRunning, nil, false, ev.GetMessage(), now)
		if err != nil {
			return err
		}
		if !moved {
			s.logger.Debug("upgrade job 已终态，忽略 INSTALLING", "job_id", jobID)
		}
	case pb.UpgradeEvent_STATUS_SUCCEEDED:
		return s.finishUpgrade(ctx, job, store.JobStatusSucceeded, 0, ev, now)
	case pb.UpgradeEvent_STATUS_ROLLED_BACK:
		// 回滚意味着升级失败（旧版已恢复）。
		return s.finishUpgrade(ctx, job, store.JobStatusFailed, -1, ev, now)
	case pb.UpgradeEvent_STATUS_FAILED:
		return s.finishUpgrade(ctx, job, store.JobStatusFailed, -1, ev, now)
	default:
		s.logger.Warn("未知升级事件状态", "job_id", jobID, "status", ev.GetStatus())
	}
	return nil
}

func (s *Service) finishUpgrade(ctx context.Context, job *store.Job,
	status string, exitCode int, ev *pb.UpgradeEvent, at time.Time) error {
	code := exitCode
	moved, err := s.store.UpdateJobState(ctx, job.JobID, status, &code, false, ev.GetMessage(), at)
	if err != nil {
		return err
	}
	if !moved {
		s.logger.Debug("upgrade job 已终态，忽略重复事件", "job_id", job.JobID)
		return nil
	}
	detail, _ := json.Marshal(map[string]any{
		"upgrade":      true,
		"from_version": ev.GetFromVersion(),
		"to_version":   ev.GetToVersion(),
		"result":       status,
		"message":      ev.GetMessage(),
	})
	return s.store.InsertAudit(ctx, store.AuditLog{
		At:      at,
		Actor:   job.RequestedBy,
		Action:  store.ActionJobFinish,
		AgentID: job.AgentID,
		JobID:   job.JobID,
		Detail:  detail,
	})
}
