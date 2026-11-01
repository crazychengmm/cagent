// Package dispatch 按标签选择器创建任务并向在线 agent 下发。
package dispatch

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/crazychengmm/cagent/internal/server/store"
	pb "github.com/crazychengmm/cagent/proto/cagent/v1"
)

const (
	// DefaultTimeout 未指定超时时的默认值（5 分钟）。
	DefaultTimeout = 5 * time.Minute
	// MaxTimeout 是单个任务的硬上限（60 分钟）。
	MaxTimeout = 60 * time.Minute
)

// ErrNoMatch 表示选择器未匹配到任何 agent。
var ErrNoMatch = errors.New("选择器未匹配到任何在线或离线 agent")

// OnlineDispatcher 是在线控制通道能力（由 controlservice.Service 满足）。
type OnlineDispatcher interface {
	OnlineAgentIDs() []string
	SendToAgent(agentID string, msg *pb.ServerMessage) error
}

// CreateRequest 是一次任务下发请求。
type CreateRequest struct {
	Selector    map[string]string
	Command     string
	WorkingDir  string
	ExtraEnv    map[string]string
	Timeout     time.Duration
	RequestedBy string
}

// CreateResult 返回本次创建的任务。
type CreateResult struct {
	JobIDs []string
}

// Service 负责任务创建与在线下发。
type Service struct {
	store  *store.Store
	online OnlineDispatcher
	now    func() time.Time
	logger *slog.Logger
}

// New 构造调度服务；online 为 nil 时只建任务不下发（测试/离线补投复用）。
func New(s *store.Store, online OnlineDispatcher, now func() time.Time, logger *slog.Logger) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{store: s, online: online, now: now, logger: logger}
}

// MatchLabels 判断 labels 是否满足 selector 的 AND 语义；空 selector 匹配所有。
func MatchLabels(labels, selector map[string]string) bool {
	for key, want := range selector {
		if labels[key] != want {
			return false
		}
	}
	return true
}

// CreateJobs 为每台匹配的 agent 创建独立 job，并对在线机立即下发。
// 离线机的 pending job 由补投流程在重连后投递。
func (s *Service) CreateJobs(ctx context.Context, req CreateRequest) (*CreateResult, error) {
	if req.Command == "" {
		return nil, errors.New("command 不能为空")
	}
	timeout := req.Timeout
	if timeout == 0 {
		timeout = DefaultTimeout
	}
	if timeout > MaxTimeout {
		return nil, errors.New("超时不能超过 60 分钟")
	}
	if timeout < 0 {
		return nil, errors.New("超时不能为负")
	}

	agents, err := s.store.ListAgents(ctx)
	if err != nil {
		return nil, err
	}

	matched := make([]store.Agent, 0)
	for _, agent := range agents {
		if MatchLabels(agent.Labels, req.Selector) {
			matched = append(matched, agent)
		}
	}
	if len(matched) == 0 {
		return nil, ErrNoMatch
	}

	now := s.now().UTC()
	result := &CreateResult{JobIDs: make([]string, 0, len(matched))}
	onlineSet := s.onlineSet()

	for _, agent := range matched {
		jobID := "job_" + uuid.New().String()
		job := store.Job{
			JobID:          jobID,
			AgentID:        agent.AgentID,
			Kind:           store.KindCommand,
			Command:        req.Command,
			WorkingDir:     req.WorkingDir,
			ExtraEnv:       req.ExtraEnv,
			TimeoutSeconds: int64(timeout / time.Second),
			RequestedBy:    req.RequestedBy,
			Status:         store.JobStatusPending,
			CreatedAt:      now,
		}
		if err := s.store.CreateJob(ctx, job); err != nil {
			return nil, err
		}
		result.JobIDs = append(result.JobIDs, jobID)

		if _, connected := onlineSet[agent.AgentID]; connected {
			dispatch := &pb.JobDispatch{
				JobId:          jobID,
				Command:        req.Command,
				WorkingDir:     req.WorkingDir,
				ExtraEnv:       req.ExtraEnv,
				TimeoutSeconds: int64(timeout / time.Second),
				RequestedBy:    req.RequestedBy,
			}
			msg := &pb.ServerMessage{Payload: &pb.ServerMessage_JobDispatch{JobDispatch: dispatch}}
			if err := s.online.SendToAgent(agent.AgentID, msg); err != nil {
				// 竞态（刚断线）：保留 pending，补投流程兜底。
				s.logger.Warn("在线下发失败，等待补投",
					"agent_id", agent.AgentID, "job_id", jobID, "error", err.Error())
			}
		}
	}

	detail, _ := json.Marshal(map[string]any{
		"selector": req.Selector,
		"command":  req.Command,
		"matched":  len(matched),
		"job_ids":  result.JobIDs,
	})
	if err := s.store.InsertAudit(ctx, store.AuditLog{
		At:     now,
		Actor:  req.RequestedBy,
		Action: store.ActionJobCreate,
		Detail: detail,
	}); err != nil {
		return nil, err
	}

	s.logger.Info("任务已创建",
		"matched", len(matched), "online", len(onlineSet), "requested_by", req.RequestedBy)
	return result, nil
}

func (s *Service) onlineSet() map[string]struct{} {
	set := make(map[string]struct{})
	if s.online != nil {
		for _, id := range s.online.OnlineAgentIDs() {
			set[id] = struct{}{}
		}
	}
	return set
}
