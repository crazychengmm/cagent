package controlservice

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/crazychengmm/cagent/internal/server/store"
	pb "github.com/crazychengmm/cagent/proto/cagent/v1"
)

// maxJobOutputBytes 单个 job 保存的输出总量上限（1MB）。
const maxJobOutputBytes = 1 << 20

// HandleJobEvent 处理 agent 回传的任务事件，推进状态机并落输出/审计。
// 未知 job 的事件只记录警告，不影响控制流（可能已过期清理）。
func (s *Service) HandleJobEvent(ctx context.Context, ev *pb.JobEvent) error {
	jobID := ev.GetJobId()
	if _, err := s.store.GetJob(ctx, jobID); errors.Is(err, store.ErrNotFound) {
		s.logger.Warn("收到未知 job 的事件，忽略", "job_id", jobID)
		return nil
	} else if err != nil {
		return err
	}

	now := s.now().UTC()
	switch e := ev.Event.(type) {
	case *pb.JobEvent_Started:
		moved, err := s.store.UpdateJobState(ctx, jobID, store.JobStatusRunning, nil, false, "", now)
		if err != nil {
			return err
		}
		if !moved {
			s.logger.Debug("job 已终态，忽略重复 Started", "job_id", jobID)
		}
	case *pb.JobEvent_Output:
		if err := s.appendOutput(ctx, jobID, e.Output); err != nil {
			return err
		}
	case *pb.JobEvent_Finished:
		return s.finishJob(ctx, jobID, e.Finished, now)
	default:
		s.logger.Warn("未知 JobEvent 类型", "job_id", jobID)
	}
	return nil
}

func (s *Service) appendOutput(ctx context.Context, jobID string, out *pb.JobOutput) error {
	streamName := store.StreamStdout
	switch out.GetStream() {
	case pb.JobOutput_STREAM_STDERR:
		streamName = store.StreamStderr
	case pb.JobOutput_STREAM_STDOUT, pb.JobOutput_STREAM_UNSPECIFIED:
	default:
		s.logger.Warn("未知输出流标识，按 stdout 处理", "job_id", jobID)
	}

	size, err := s.store.SumJobOutputSize(ctx, jobID)
	if err != nil {
		return err
	}
	if size >= maxJobOutputBytes {
		s.logger.Warn("输出超过上限，丢弃后续块",
			"job_id", jobID, "size", size, "limit", maxJobOutputBytes)
		return nil
	}

	chunks, err := s.store.GetJobOutput(ctx, jobID)
	if err != nil {
		return err
	}
	chunk := store.JobOutputChunk{
		Seq:    int64(len(chunks)) + 1,
		Stream: streamName,
		Data:   out.GetData(),
	}
	// 单块可能把总量推过上限：截断块内字节，保证总量不超过 1MB。
	if size+int64(len(chunk.Data)) > maxJobOutputBytes {
		chunk.Data = chunk.Data[:maxJobOutputBytes-size]
	}
	return s.store.AppendJobOutput(ctx, jobID, chunk)
}

func (s *Service) finishJob(ctx context.Context, jobID string,
	fin *pb.JobFinished, at time.Time) error {
	result := mapFinishedResult(fin.GetResult())

	size, err := s.store.SumJobOutputSize(ctx, jobID)
	if err != nil {
		return err
	}
	truncated := fin.GetTruncated() || size >= maxJobOutputBytes

	var exitCode *int
	if fin.GetExitCode() >= 0 {
		code := int(fin.GetExitCode())
		exitCode = &code
	}
	moved, err := s.store.UpdateJobState(ctx, jobID, result, exitCode, truncated, fin.GetMessage(), at)
	if err != nil {
		return err
	}
	if !moved {
		s.logger.Debug("job 已终态，忽略重复 Finished", "job_id", jobID)
		return nil
	}

	job, err := s.store.GetJob(ctx, jobID)
	if err != nil {
		return err
	}
	detail, _ := json.Marshal(map[string]any{
		"command":   job.Command,
		"result":    result,
		"exit_code": fin.GetExitCode(),
		"truncated": truncated,
	})
	return s.store.InsertAudit(ctx, store.AuditLog{
		At:      at,
		Actor:   job.RequestedBy,
		Action:  store.ActionJobFinish,
		AgentID: job.AgentID,
		JobID:   jobID,
		Detail:  detail,
	})
}

func mapFinishedResult(r pb.JobFinished_Result) string {
	switch r {
	case pb.JobFinished_RESULT_SUCCEEDED:
		return store.JobStatusSucceeded
	case pb.JobFinished_RESULT_FAILED:
		return store.JobStatusFailed
	case pb.JobFinished_RESULT_TIMEOUT:
		return store.JobStatusTimeout
	case pb.JobFinished_RESULT_CANCELED:
		return store.JobStatusCanceled
	default:
		return store.JobStatusFailed
	}
}
