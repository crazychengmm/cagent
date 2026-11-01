// Package serve 组装并运行 server 的三个 gRPC 端口：
// enrollment（单向 TLS）、control（mTLS）、admin（admin token 拦截器）。
package serve

import (
	"context"
	"crypto/tls"
	"errors"
	"log/slog"
	"net"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	"github.com/crazychengmm/cagent/internal/server/admin"
	"github.com/crazychengmm/cagent/internal/server/store"
	"github.com/crazychengmm/cagent/internal/server/tlsconfig"
	pb "github.com/crazychengmm/cagent/proto/cagent/v1"
)

const gracefulShutdownTimeout = 10 * time.Second

// Deps 是 Run 的全部装配依赖。
type Deps struct {
	EnrollmentListen string
	ControlListen    string
	AdminListen      string

	EnrollService pb.EnrollmentServiceServer

	ServerCert tls.Certificate
	CAPEM      []byte
	AdminToken string
	Store      *store.Store
	Logger     *slog.Logger

	// RegisterControl/RegisterAdmin 在对应 grpc.Server 上挂载业务服务，
	// 由后续任务（控制通道、ctl API）注入；为 nil 时只起空端口。
	RegisterControl func(*grpc.Server)
	RegisterAdmin   func(*grpc.Server)
}

type managedServer struct {
	name   string
	server *grpc.Server
	lis    net.Listener
}

// Run 监听三个端口并阻塞服务，直到 ctx 取消后优雅停止；启动期监听失败立即返回。
func Run(ctx context.Context, d Deps) error {
	if d.Logger == nil {
		return errors.New("logger 不能为空")
	}

	enrollmentServer, err := d.buildEnrollmentServer()
	if err != nil {
		return err
	}
	controlServer, err := d.buildControlServer()
	if err != nil {
		return err
	}
	adminServer := d.buildAdminServer()

	servers := make([]*managedServer, 0, 3)
	for _, item := range []struct {
		name   string
		addr   string
		server *grpc.Server
	}{
		{"enrollment", d.EnrollmentListen, enrollmentServer},
		{"control", d.ControlListen, controlServer},
		{"admin", d.AdminListen, adminServer},
	} {
		lis, err := net.Listen("tcp", item.addr)
		if err != nil {
			for _, started := range servers {
				started.server.Stop()
			}
			return err
		}
		servers = append(servers, &managedServer{name: item.name, server: item.server, lis: lis})
		d.Logger.Info("开始监听", "port", item.name, "addr", lis.Addr().String())
	}

	var wg sync.WaitGroup
	for _, ms := range servers {
		wg.Add(1)
		go func(ms *managedServer) {
			defer wg.Done()
			if err := ms.server.Serve(ms.lis); err != nil && !errors.Is(err, grpc.ErrServerStopped) {
				d.Logger.Error("gRPC 服务异常退出", "port", ms.name, "error", err.Error())
			}
		}(ms)
	}

	<-ctx.Done()
	d.Logger.Info("收到退出信号，开始优雅停止")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), gracefulShutdownTimeout)
	defer cancel()

	stopDone := make(chan struct{})
	go func() {
		for _, ms := range servers {
			ms.server.GracefulStop()
		}
		close(stopDone)
	}()
	select {
	case <-stopDone:
	case <-shutdownCtx.Done():
		for _, ms := range servers {
			ms.server.Stop()
		}
	}
	wg.Wait()
	return nil
}

func (d Deps) buildEnrollmentServer() (*grpc.Server, error) {
	creds := credentials.NewTLS(tlsconfig.Enrollment(d.ServerCert))
	srv := grpc.NewServer(grpc.Creds(creds))
	pb.RegisterEnrollmentServiceServer(srv, d.EnrollService)
	return srv, nil
}

func (d Deps) buildControlServer() (*grpc.Server, error) {
	tlsCfg, err := tlsconfig.Control(d.ServerCert, d.CAPEM, d.Store.IsAgentCertificateActive)
	if err != nil {
		return nil, err
	}
	srv := grpc.NewServer(grpc.Creds(credentials.NewTLS(tlsCfg)))
	if d.RegisterControl != nil {
		d.RegisterControl(srv)
	}
	return srv, nil
}

func (d Deps) buildAdminServer() *grpc.Server {
	interceptor := admin.NewAuthInterceptor(d.AdminToken)
	// admin 端口同样使用服务端 TLS（单向，客户端用 CA 锚点或等效校验），
	// 再由拦截器强制 admin token。
	srv := grpc.NewServer(
		grpc.Creds(credentials.NewTLS(tlsconfig.Enrollment(d.ServerCert))),
		grpc.UnaryInterceptor(interceptor.Unary),
		grpc.StreamInterceptor(interceptor.Stream),
	)
	if d.RegisterAdmin != nil {
		d.RegisterAdmin(srv)
	}
	return srv
}
