// Package enrollment 实现注册引导：enrollment token 的签发/校验与注册受理。
package enrollment

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/crazychengmm/cagent/internal/server/store"
)

// token 校验的可预期失败，调用方用 errors.Is 区分。
var (
	ErrInvalidToken = errors.New("enrollment token 无效")
	ErrTokenRevoked = errors.New("enrollment token 已吊销")
	ErrTokenExpired = errors.New("enrollment token 已过期")
)

const (
	// tokenRandomBytes 明文令牌的随机字节数（≥32）。
	tokenRandomBytes = 32
	tokenPrefix      = "cagt_"
	tokenIDBytes     = 8
)

// TokenService 管理 enrollment token 的创建、校验、列表与吊销。
type TokenService struct {
	store      *store.Store
	now        func() time.Time
	defaultTTL time.Duration
}

// NewTokenService 构造服务；now 为 nil 时用墙上时钟。
func NewTokenService(s *store.Store, defaultTTL time.Duration, now func() time.Time) *TokenService {
	if now == nil {
		now = time.Now
	}
	return &TokenService{store: s, defaultTTL: defaultTTL, now: now}
}

// CreateToken 生成一枚新令牌，返回仅显示一次的明文与持久化记录。
// ttl<=0 时采用服务默认 TTL。明文可被多台机器在有效期内重复使用。
func (svc *TokenService) CreateToken(ctx context.Context,
	description, createdBy string, ttl time.Duration) (string, *store.EnrollmentToken, error) {
	if ttl <= 0 {
		ttl = svc.defaultTTL
	}
	if ttl <= 0 {
		return "", nil, errors.New("token TTL 必须为正数")
	}

	secret, err := randomText(tokenRandomBytes)
	if err != nil {
		return "", nil, err
	}
	id, err := randomID()
	if err != nil {
		return "", nil, err
	}

	now := svc.now().UTC()
	record := store.EnrollmentToken{
		ID:          id,
		Description: description,
		CreatedBy:   createdBy,
		CreatedAt:   now,
		ExpiresAt:   now.Add(ttl),
	}
	plaintext := tokenPrefix + secret
	if err := svc.store.CreateEnrollmentToken(ctx, record, plaintext); err != nil {
		return "", nil, err
	}
	return plaintext, &record, nil
}

// Validate 校验明文令牌，返回有效记录；失败原因由哨兵错误区分。
func (svc *TokenService) Validate(ctx context.Context,
	plaintext string) (*store.EnrollmentToken, error) {
	if plaintext == "" {
		return nil, ErrInvalidToken
	}
	token, err := svc.store.GetEnrollmentTokenByPlaintext(ctx, plaintext)
	if errors.Is(err, store.ErrNotFound) {
		return nil, ErrInvalidToken
	}
	if err != nil {
		return nil, err
	}

	now := svc.now()
	if token.RevokedAt != nil {
		return nil, ErrTokenRevoked
	}
	if now.After(token.ExpiresAt) {
		return nil, ErrTokenExpired
	}
	return token, nil
}

// MarkUsed 在注册成功后记录令牌使用时间（可复用，不消耗）。
func (svc *TokenService) MarkUsed(ctx context.Context, id string) error {
	return svc.store.TouchEnrollmentToken(ctx, id, svc.now().UTC())
}

// ListTokens 返回全部令牌元数据（不含明文）。
func (svc *TokenService) ListTokens(ctx context.Context) ([]store.EnrollmentToken, error) {
	return svc.store.ListEnrollmentTokens(ctx)
}

// RevokeToken 吊销令牌；不存在或已吊销返回 false。
func (svc *TokenService) RevokeToken(ctx context.Context, id string) (bool, error) {
	return svc.store.RevokeEnrollmentToken(ctx, id, svc.now().UTC())
}

func randomText(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("生成随机令牌: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func randomID() (string, error) {
	buf := make([]byte, tokenIDBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("生成令牌 ID: %w", err)
	}
	return "et_" + hex.EncodeToString(buf), nil
}
