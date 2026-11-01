package admin

import (
	"context"
	"crypto/subtle"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

const metadataKey = "authorization"

// AuthInterceptor 校验管理 API 请求中的 admin token。
type AuthInterceptor struct {
	token string
}

// NewAuthInterceptor 构造拦截器；token 为空时所有请求都会被拒绝。
func NewAuthInterceptor(token string) *AuthInterceptor {
	return &AuthInterceptor{token: strings.TrimSpace(token)}
}

func (a *AuthInterceptor) authorize(ctx context.Context) error {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return status.Error(codes.Unauthenticated, "缺少请求元数据")
	}
	values := md.Get(metadataKey)
	if len(values) == 0 {
		return status.Error(codes.Unauthenticated, "缺少 admin token")
	}

	presented := strings.TrimSpace(values[0])
	if strings.HasPrefix(strings.ToLower(presented), "bearer ") {
		presented = strings.TrimSpace(presented[7:])
	}
	if presented == "" || subtle.ConstantTimeCompare([]byte(presented), []byte(a.token)) != 1 {
		return status.Error(codes.Unauthenticated, "admin token 无效")
	}
	return nil
}

// Unary 是 gRPC 一元拦截器。
func (a *AuthInterceptor) Unary(ctx context.Context, req any,
	info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	if err := a.authorize(ctx); err != nil {
		return nil, err
	}
	return handler(ctx, req)
}

// Stream 是 gRPC 流拦截器。
func (a *AuthInterceptor) Stream(srv any, ss grpc.ServerStream,
	info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
	if err := a.authorize(ss.Context()); err != nil {
		return err
	}
	return handler(srv, ss)
}
