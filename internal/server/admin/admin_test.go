package admin

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func TestEnsureAdminToken_GenerateAndReuse(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	token, err := EnsureAdminToken(dir)
	if err != nil {
		t.Fatalf("首次生成: %v", err)
	}
	if !strings.HasPrefix(token, adminTokenPrefix) || len(token) <= len(adminTokenPrefix) {
		t.Errorf("token 形态非法: %q", token)
	}

	info, err := os.Stat(filepath.Join(dir, adminTokenFile))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != adminFilePerm {
		t.Errorf("文件权限 = %o, 期望 %o", info.Mode().Perm(), adminFilePerm)
	}

	again, err := EnsureAdminToken(dir)
	if err != nil {
		t.Fatalf("二次读取: %v", err)
	}
	if again != token {
		t.Error("重复启动必须复用既有 token，而非重新生成")
	}
}

func TestEnsureAdminToken_RejectsEmptyFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, adminTokenFile), []byte("  \n"), adminFilePerm); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureAdminToken(dir); err == nil {
		t.Fatal("空 token 文件应报错")
	}
}

func callUnary(t *testing.T, ic *grpc.UnaryServerInfo, ctx context.Context, tokenHeader string) error {
	t.Helper()
	interceptor := NewAuthInterceptor("cactl_secret")
	if tokenHeader != "" {
		ctx = metadata.NewIncomingContext(ctx, metadata.Pairs(metadataKey, tokenHeader))
	}
	called := false
	_, err := interceptor.Unary(ctx, nil, ic,
		func(ctx context.Context, req any) (any, error) { called = true; return "ok", nil })
	if err == nil && !called {
		t.Fatal("放行时 handler 必须被调用")
	}
	return err
}

var dummyUnaryInfo = &grpc.UnaryServerInfo{FullMethod: "/admin.v1.Test/Do"}

func TestAuthInterceptor(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	if got := callUnary(t, dummyUnaryInfo, ctx, ""); status.Code(got) != codes.Unauthenticated {
		t.Errorf("无 header 应 Unauthenticated，实际 %v", got)
	}
	if got := callUnary(t, dummyUnaryInfo, ctx, "cactl_wrong"); status.Code(got) != codes.Unauthenticated {
		t.Errorf("错误 token 应 Unauthenticated，实际 %v", got)
	}
	if got := callUnary(t, dummyUnaryInfo, ctx, "Bearer cactl_wrong"); status.Code(got) != codes.Unauthenticated {
		t.Errorf("错误 bearer 应 Unauthenticated，实际 %v", got)
	}
	if got := callUnary(t, dummyUnaryInfo, ctx, "cactl_secret"); got != nil {
		t.Errorf("裸 token 正确应放行，实际 %v", got)
	}
	if got := callUnary(t, dummyUnaryInfo, ctx, "Bearer cactl_secret"); got != nil {
		t.Errorf("bearer token 正确应放行，实际 %v", got)
	}
}

// fakeServerStream 是只提供 Context 的最小 grpc.ServerStream。
type fakeServerStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (f *fakeServerStream) Context() context.Context { return f.ctx }

func TestStreamInterceptor_RejectsMissingToken(t *testing.T) {
	t.Parallel()

	interceptor := NewAuthInterceptor("cactl_secret")
	err := interceptor.Stream(context.Background(), &fakeServerStream{ctx: context.Background()},
		&grpc.StreamServerInfo{}, func(srv any, ss grpc.ServerStream) error {
			t.Fatal("无 token 时 handler 不应被调用")
			return nil
		})
	if status.Code(err) != codes.Unauthenticated {
		t.Errorf("无元数据流应 Unauthenticated，实际 %v", err)
	}
}

func TestStreamInterceptor_AcceptsValidToken(t *testing.T) {
	t.Parallel()

	interceptor := NewAuthInterceptor("cactl_secret")
	ctx := metadata.NewIncomingContext(context.Background(),
		metadata.Pairs(metadataKey, "Bearer cactl_secret"))
	called := false
	err := interceptor.Stream(context.Background(), &fakeServerStream{ctx: ctx},
		&grpc.StreamServerInfo{}, func(srv any, ss grpc.ServerStream) error {
			called = true
			return nil
		})
	if err != nil || !called {
		t.Errorf("有效 token 应放行，called=%v err=%v", called, err)
	}
}
