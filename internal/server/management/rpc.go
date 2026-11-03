package management

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/crazychengmm/cagent/internal/server/controlservice"
	"github.com/crazychengmm/cagent/internal/server/dispatch"
	"github.com/crazychengmm/cagent/internal/server/store"
	pb "github.com/crazychengmm/cagent/proto/cagent/v1"
)

// ---- enrollment token ----

func (s *Service) CreateToken(ctx context.Context,
	req *pb.CreateTokenRequest) (*pb.CreateTokenResponse, error) {
	if s.tokens == nil {
		return nil, status.Error(codes.FailedPrecondition, "token 服务未配置")
	}
	ttl := 7 * 24 * time.Hour
	if req.GetTtlSeconds() > 0 {
		ttl = time.Duration(req.GetTtlSeconds()) * time.Second
	}
	plaintext, record, err := s.tokens.CreateToken(ctx, req.GetDescription(), actorFromContext(ctx), ttl)
	if err != nil {
		return nil, status.Error(codes.Internal, "创建 token 失败")
	}
	detail, _ := json.Marshal(map[string]any{
		"token_id": record.ID, "description": record.Description, "ttl_seconds": int64(ttl.Seconds())})
	_ = s.store.InsertAudit(ctx, store.AuditLog{
		At: s.now().UTC(), Actor: actorFromContext(ctx),
		Action: store.ActionTokenCreate, Detail: detail,
	})
	return &pb.CreateTokenResponse{Plaintext: plaintext, Token: toTokenInfo(record)}, nil
}

func (s *Service) ListTokens(ctx context.Context,
	_ *pb.ListTokensRequest) (*pb.ListTokensResponse, error) {
	tokens, err := s.store.ListEnrollmentTokens(ctx)
	if err != nil {
		return nil, status.Error(codes.Internal, "查询 token 失败")
	}
	infos := make([]*pb.TokenInfo, 0, len(tokens))
	for i := range tokens {
		infos = append(infos, toTokenInfo(&tokens[i]))
	}
	return &pb.ListTokensResponse{Tokens: infos}, nil
}

func (s *Service) RevokeToken(ctx context.Context,
	req *pb.RevokeTokenRequest) (*pb.RevokeTokenResponse, error) {
	if ok, err := s.store.RevokeEnrollmentToken(ctx, req.GetId(), s.now().UTC()); err != nil {
		return nil, status.Errorf(codes.Internal, "吊销失败: %v", err)
	} else if !ok {
		return nil, status.Errorf(codes.NotFound, "token %s 不存在", req.GetId())
	}
	detail, _ := json.Marshal(map[string]any{"token_id": req.GetId()})
	_ = s.store.InsertAudit(ctx, store.AuditLog{
		At: s.now().UTC(), Actor: actorFromContext(ctx),
		Action: store.ActionTokenRevoke, Detail: detail,
	})
	return &pb.RevokeTokenResponse{}, nil
}

func toTokenInfo(t *store.EnrollmentToken) *pb.TokenInfo {
	info := &pb.TokenInfo{
		Id:          t.ID,
		Description: t.Description,
		CreatedBy:   t.CreatedBy,
		CreatedAt:   timestamppb.New(t.CreatedAt),
		ExpiresAt:   timestamppb.New(t.ExpiresAt),
	}
	if t.RevokedAt != nil {
		info.Revoked = true
	}
	if t.LastUsedAt != nil {
		info.LastUsedAt = timestamppb.New(*t.LastUsedAt)
	}
	return info
}

// ---- agent ----

func (s *Service) ListAgents(ctx context.Context,
	_ *pb.ListAgentsRequest) (*pb.ListAgentsResponse, error) {
	agents, err := s.store.ListAgents(ctx)
	if err != nil {
		return nil, status.Error(codes.Internal, "查询 agent 失败")
	}
	infos := make([]*pb.AgentInfo, 0, len(agents))
	for i := range agents {
		infos = append(infos, toAgentInfo(&agents[i]))
	}
	return &pb.ListAgentsResponse{Agents: infos}, nil
}

func (s *Service) ShowAgent(ctx context.Context,
	req *pb.ShowAgentRequest) (*pb.ShowAgentResponse, error) {
	agent, err := s.store.GetAgent(ctx, req.GetId())
	if errors.Is(err, store.ErrNotFound) {
		return nil, status.Errorf(codes.NotFound, "agent %s 不存在", req.GetId())
	}
	if err != nil {
		return nil, status.Error(codes.Internal, "查询失败")
	}
	return &pb.ShowAgentResponse{Agent: toAgentInfo(agent)}, nil
}

func toAgentInfo(a *store.Agent) *pb.AgentInfo {
	info := &pb.AgentInfo{
		Id:       a.AgentID,
		Hostname: a.Hostname,
		Labels:   a.Labels,
		Version:  a.Version,
		Status:   a.Status,
	}
	if a.LastHeartbeatAt != nil {
		info.LastHeartbeatAt = timestamppb.New(*a.LastHeartbeatAt)
	}
	if a.CertNotAfter != nil {
		info.CertNotAfter = timestamppb.New(*a.CertNotAfter)
	}
	info.CreatedAt = timestamppb.New(a.CreatedAt)
	return info
}

// ---- job ----

func (s *Service) RunJob(ctx context.Context,
	req *pb.RunJobRequest) (*pb.RunJobResponse, error) {
	if s.jobCreator == nil {
		return nil, status.Error(codes.FailedPrecondition, "任务服务未配置")
	}
	var timeout time.Duration
	if req.GetTimeoutSeconds() > 0 {
		timeout = time.Duration(req.GetTimeoutSeconds()) * time.Second
	}
	result, err := s.jobCreator.CreateJobs(ctx, dispatch.CreateRequest{
		Selector:    req.GetSelector(),
		Command:     req.GetCommand(),
		WorkingDir:  req.GetWorkingDir(),
		ExtraEnv:    req.GetExtraEnv(),
		Timeout:     timeout,
		RequestedBy: actorFromContext(ctx),
	})
	if errors.Is(err, dispatch.ErrNoMatch) {
		return nil, status.Error(codes.FailedPrecondition, "选择器未匹配到任何 agent")
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "创建任务失败: %v", err)
	}
	return &pb.RunJobResponse{JobIds: result.JobIDs, Matched: int32(len(result.JobIDs))}, nil
}

func (s *Service) ListJobs(ctx context.Context,
	req *pb.ListJobsRequest) (*pb.ListJobsResponse, error) {
	jobs, err := s.store.ListJobs(ctx, store.ListJobsFilter{
		AgentID: req.GetAgentId(),
		Status:  req.GetStatus(),
		Limit:   int(req.GetLimit()),
	})
	if err != nil {
		return nil, status.Error(codes.Internal, "查询任务失败")
	}
	infos := make([]*pb.JobInfo, 0, len(jobs))
	for i := range jobs {
		infos = append(infos, toJobInfo(&jobs[i]))
	}
	return &pb.ListJobsResponse{Jobs: infos}, nil
}

func (s *Service) ShowJob(ctx context.Context,
	req *pb.ShowJobRequest) (*pb.ShowJobResponse, error) {
	job, err := s.store.GetJob(ctx, req.GetId())
	if errors.Is(err, store.ErrNotFound) {
		return nil, status.Errorf(codes.NotFound, "job %s 不存在", req.GetId())
	}
	if err != nil {
		return nil, status.Error(codes.Internal, "查询失败")
	}
	chunks, err := s.store.GetJobOutput(ctx, req.GetId())
	if err != nil {
		return nil, status.Error(codes.Internal, "查询输出失败")
	}
	outputs := make([]*pb.OutputChunkInfo, 0, len(chunks))
	for _, c := range chunks {
		outputs = append(outputs, &pb.OutputChunkInfo{
			Seq: c.Seq, Stream: c.Stream, Data: c.Data,
		})
	}
	return &pb.ShowJobResponse{Job: toJobInfo(job), Output: outputs}, nil
}

func (s *Service) CancelJob(ctx context.Context,
	req *pb.CancelJobRequest) (*pb.CancelJobResponse, error) {
	job, err := s.store.GetJob(ctx, req.GetId())
	if errors.Is(err, store.ErrNotFound) {
		return nil, status.Errorf(codes.NotFound, "job %s 不存在", req.GetId())
	}
	if err != nil {
		return nil, status.Error(codes.Internal, "查询失败")
	}
	if s.online == nil {
		return nil, status.Error(codes.Unavailable, "控制通道未配置")
	}
	err = s.online.SendToAgent(job.AgentID, &pb.ServerMessage{
		Payload: &pb.ServerMessage_JobCancel{JobCancel: &pb.JobCancel{JobId: req.GetId()}},
	})
	if errors.Is(err, controlservice.ErrAgentOffline) {
		return nil, status.Errorf(codes.Unavailable, "agent %s 不在线，无法取消", job.AgentID)
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "下发取消失败: %v", err)
	}
	// 取消是显式运维操作，记录审计。
	detail, _ := json.Marshal(map[string]any{
		"job_id":   job.JobID,
		"agent_id": job.AgentID,
		"command":  job.Command,
	})
	_ = s.store.InsertAudit(ctx, store.AuditLog{
		At:      s.now().UTC(),
		Actor:   actorFromContext(ctx),
		Action:  "job.cancel",
		AgentID: job.AgentID,
		JobID:   job.JobID,
		Detail:  detail,
	})
	return &pb.CancelJobResponse{}, nil
}

func toJobInfo(j *store.Job) *pb.JobInfo {
	info := &pb.JobInfo{
		JobId:          j.JobID,
		AgentId:        j.AgentID,
		Kind:           j.Kind,
		Command:        j.Command,
		WorkingDir:     j.WorkingDir,
		ExtraEnv:       j.ExtraEnv,
		TimeoutSeconds: j.TimeoutSeconds,
		RequestedBy:    j.RequestedBy,
		Status:         j.Status,
		Truncated:      j.Truncated,
		Message:        j.Message,
		CreatedAt:      timestamppb.New(j.CreatedAt),
	}
	if j.ExitCode != nil {
		code := int32(*j.ExitCode)
		info.ExitCode = &code
	}
	if j.StartedAt != nil {
		info.StartedAt = timestamppb.New(*j.StartedAt)
	}
	if j.FinishedAt != nil {
		info.FinishedAt = timestamppb.New(*j.FinishedAt)
	}
	return info
}
