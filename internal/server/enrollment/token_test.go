package enrollment

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crazychengmm/cagent/internal/server/store"
)

func newTestTokenService(t *testing.T, defaultTTL time.Duration) (*TokenService, *time.Time) {
	t.Helper()
	s, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	clock := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	svc := NewTokenService(s, defaultTTL, func() time.Time { return clock })
	return svc, &clock
}

func TestCreateToken_FormatAndDistinct(t *testing.T) {
	t.Parallel()

	svc, _ := newTestTokenService(t, 168*time.Hour)
	ctx := context.Background()

	p1, rec1, err := svc.CreateToken(ctx, "批 A", "admin", time.Hour)
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}
	p2, _, err := svc.CreateToken(ctx, "批 B", "admin", time.Hour)
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}
	if !strings.HasPrefix(p1, tokenPrefix) {
		t.Errorf("明文应带前缀 %s，实际 %q", tokenPrefix, p1)
	}
	if len(p1) != len(tokenPrefix)+43 { // 32 字节 base64url 无填充 = 43
		t.Errorf("明文长度 = %d, 期望 %d", len(p1), len(tokenPrefix)+43)
	}
	if p1 == p2 {
		t.Error("两枚令牌明文不应相同")
	}
	if rec1.ID == "" || !strings.HasPrefix(rec1.ID, "et_") {
		t.Errorf("令牌 ID 非法: %q", rec1.ID)
	}
}

func TestValidateToken_ReusableUntilExpiryOrRevoke(t *testing.T) {
	t.Parallel()

	svc, clock := newTestTokenService(t, 168*time.Hour)
	ctx := context.Background()

	const ttl = time.Hour
	plaintext, rec, err := svc.CreateToken(ctx, "", "admin", ttl)
	if err != nil {
		t.Fatal(err)
	}

	// 有效期内可重复校验。
	for i := 0; i < 2; i++ {
		got, err := svc.Validate(ctx, plaintext)
		if err != nil || got.ID != rec.ID {
			t.Fatalf("第 %d 次校验失败: %v", i+1, err)
		}
	}

	if _, err := svc.Validate(ctx, ""); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("空令牌应 ErrInvalidToken，实际 %v", err)
	}
	if _, err := svc.Validate(ctx, tokenPrefix+"wrong"); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("未知令牌应 ErrInvalidToken，实际 %v", err)
	}

	// 过期。
	*clock = clock.Add(ttl + time.Second)
	if _, err := svc.Validate(ctx, plaintext); !errors.Is(err, ErrTokenExpired) {
		t.Errorf("过期应 ErrTokenExpired，实际 %v", err)
	}
}

func TestValidateToken_Revoked(t *testing.T) {
	t.Parallel()

	svc, _ := newTestTokenService(t, 168*time.Hour)
	ctx := context.Background()
	plaintext, rec, err := svc.CreateToken(ctx, "", "admin", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	ok, err := svc.RevokeToken(ctx, rec.ID)
	if err != nil || !ok {
		t.Fatalf("RevokeToken ok=%v err=%v", ok, err)
	}
	if _, err := svc.Validate(ctx, plaintext); !errors.Is(err, ErrTokenRevoked) {
		t.Errorf("吊销后应 ErrTokenRevoked，实际 %v", err)
	}
}

func TestCreateToken_DefaultTTL(t *testing.T) {
	t.Parallel()

	defaultTTL := 168 * time.Hour
	svc, clock := newTestTokenService(t, defaultTTL)
	_, rec, err := svc.CreateToken(context.Background(), "", "admin", 0)
	if err != nil {
		t.Fatal(err)
	}
	want := clock.Add(defaultTTL)
	if !rec.ExpiresAt.Equal(want) {
		t.Errorf("默认 TTL 过期时间 = %s, 期望 %s", rec.ExpiresAt, want)
	}
}

func TestMarkUsed(t *testing.T) {
	t.Parallel()

	svc, _ := newTestTokenService(t, 168*time.Hour)
	ctx := context.Background()
	_, rec, err := svc.CreateToken(ctx, "", "admin", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.MarkUsed(ctx, rec.ID); err != nil {
		t.Fatalf("MarkUsed: %v", err)
	}
	list, err := svc.ListTokens(ctx)
	if err != nil || len(list) != 1 || list[0].LastUsedAt == nil {
		t.Fatalf("使用记录未更新: %+v, %v", list, err)
	}
}
