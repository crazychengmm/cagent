// Package enrollservice 实现 gRPC EnrollmentService：校验引导令牌与 CSR，
// 分配持久 UUID，由内置 CA 签发 agent 身份证书并登记 agents 表。
package enrollservice

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/crazychengmm/cagent/internal/server/ca"
	"github.com/crazychengmm/cagent/internal/server/enrollment"
	"github.com/crazychengmm/cagent/internal/server/store"
	pb "github.com/crazychengmm/cagent/proto/cagent/v1"
)

// 注册请求的体量限制（C-SEC-01：外部输入限定长度）。
const (
	maxHostnameLength = 255
	maxLabelCount     = 32
	maxLabelKeyLength = 128
	maxLabelValueLen  = 256
)

// Service 是 EnrollmentService 的 gRPC 实现。
type Service struct {
	pb.UnimplementedEnrollmentServiceServer

	store        *store.Store
	tokens       *enrollment.TokenService
	ca           *ca.CA
	certValidity time.Duration
	now          func() time.Time
	logger       *slog.Logger
}

// New 构造注册服务；now 为 nil 时用墙上时钟。
func New(s *store.Store, tokens *enrollment.TokenService, rootCA *ca.CA,
	certValidity time.Duration, now func() time.Time, logger *slog.Logger) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{
		store:        s,
		tokens:       tokens,
		ca:           rootCA,
		certValidity: certValidity,
		now:          now,
		logger:       logger,
	}
}

// Enroll 见 proto/cagent/v1/enrollment.proto。
func (s *Service) Enroll(ctx context.Context,
	req *pb.EnrollRequest) (*pb.EnrollResponse, error) {
	if err := validateEnrollRequest(req); err != nil {
		s.auditRejected(ctx, "", err.Error())
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	token, err := s.tokens.Validate(ctx, req.GetEnrollmentToken())
	if err != nil {
		s.auditRejected(ctx, "", "enrollment token 校验失败: "+reason(err))
		return nil, status.Error(codes.PermissionDenied, "enrollment token 无效、已过期或已吊销")
	}

	agentID := uuid.NewString()
	now := s.now().UTC()
	notAfter := now.Add(s.certValidity)
	serial, err := ca.NewSerial()
	if err != nil {
		return nil, status.Error(codes.Internal, "生成证书序列号失败")
	}

	certPEM, err := s.ca.SignAgentCSR(req.GetCsrPem(), agentID, serial, notAfter)
	if err != nil {
		s.auditRejected(ctx, token.CreatedBy, "CSR 处理失败: "+err.Error())
		return nil, status.Error(codes.InvalidArgument, "CSR 无效: "+err.Error())
	}

	record := store.Agent{
		AgentID:           agentID,
		Hostname:          req.GetHostname(),
		Labels:            req.GetLabels(),
		Status:            store.StatusOffline,
		CertificateSerial: serial.Text(16),
		CertNotAfter:      &notAfter,
		CreatedAt:         now,
		UpdatedAt:         now,
	}
	if err := s.store.InsertAgent(ctx, record); err != nil {
		return nil, status.Error(codes.Internal, "登记 agent 失败")
	}
	if err := s.tokens.MarkUsed(ctx, token.ID); err != nil {
		// 不致命：身份已签发，仅影响使用统计。
		s.logger.Warn("更新 token 使用时间失败", "token_id", token.ID, "error", err.Error())
	}

	detail, _ := json.Marshal(map[string]any{
		"hostname": req.GetHostname(),
		"labels":   req.GetLabels(),
		"token_id": token.ID,
	})
	if err := s.store.InsertAudit(ctx, store.AuditLog{
		At:      now,
		Actor:   actorFromToken(token.CreatedBy),
		Action:  store.ActionAgentEnroll,
		AgentID: agentID,
		Detail:  detail,
	}); err != nil {
		s.logger.Warn("写注册审计失败", "agent_id", agentID, "error", err.Error())
	}

	s.logger.Info("agent 注册成功", "agent_id", agentID, "hostname", req.GetHostname())
	return &pb.EnrollResponse{
		AgentId:          agentID,
		CertificatePem:   certPEM,
		CaCertificatePem: s.ca.CACertPEM(),
		NotAfter:         timestamppb.New(notAfter),
	}, nil
}

func validateEnrollRequest(req *pb.EnrollRequest) error {
	if req.GetEnrollmentToken() == "" {
		return errors.New("enrollment_token 不能为空")
	}
	if len(req.GetCsrPem()) == 0 {
		return errors.New("csr_pem 不能为空")
	}
	if hostname := req.GetHostname(); len(hostname) == 0 || len(hostname) > maxHostnameLength {
		return errors.New("hostname 长度必须在 1..255 之间")
	}
	labels := req.GetLabels()
	if len(labels) > maxLabelCount {
		return errors.New("标签数量超过上限 32")
	}
	for key, value := range labels {
		if len(key) == 0 || len(key) > maxLabelKeyLength {
			return errors.New("标签键长度必须在 1..128 之间")
		}
		if len(value) == 0 || len(value) > maxLabelValueLen {
			return errors.New("标签值长度必须在 1..256 之间")
		}
	}
	return nil
}

func (s *Service) auditRejected(ctx context.Context, actor, reason string) {
	detail, _ := json.Marshal(map[string]any{"reason": reason})
	if err := s.store.InsertAudit(ctx, store.AuditLog{
		At:     s.now().UTC(),
		Actor:  actorFromToken(actor),
		Action: store.ActionAgentRejected,
		Detail: detail,
	}); err != nil {
		s.logger.Warn("写拒绝审计失败", "error", err.Error())
	}
}

func actorFromToken(createdBy string) string {
	if createdBy == "" {
		return "enrollment-token"
	}
	return createdBy
}

func reason(err error) string {
	switch {
	case errors.Is(err, enrollment.ErrTokenRevoked):
		return "已吊销"
	case errors.Is(err, enrollment.ErrTokenExpired):
		return "已过期"
	default:
		return "无效"
	}
}
