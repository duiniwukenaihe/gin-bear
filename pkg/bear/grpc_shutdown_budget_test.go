package bear

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
)

func TestGRPCServerIsStoppedWhenHTTPBindingFails(t *testing.T) {
	httpBlocker, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	defer httpBlocker.Close()
	httpPort := httpBlocker.Addr().(*net.TCPAddr).Port
	_, grpcPort := grpcServeUnusedPorts(t)
	config := grpcServeConfig(httpPort, grpcPort, time.Second)
	app := grpcServeNewApp(t, config)
	service := &grpcServerCaptureService{grpcServeService: grpcServeService{}}
	if err := app.AddGRPCServiceE(service); err != nil {
		t.Fatalf("AddGRPCServiceE() error = %v", err)
	}

	err = app.Serve(context.Background())
	if err == nil {
		t.Fatal("Serve() error = nil, want HTTP bind failure")
	}
	if service.server == nil {
		t.Fatal("gRPC service did not capture the built server")
	}

	assertCapturedGRPCServerStopped(t, service.server)
}

func TestGRPCServerIsStoppedWhenGRPCBindingFails(t *testing.T) {
	grpcBlocker, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer grpcBlocker.Close()
	grpcPort := grpcBlocker.Addr().(*net.TCPAddr).Port
	httpPort, _ := grpcServeUnusedPorts(t)
	config := grpcServeConfig(httpPort, grpcPort, time.Second)
	app := grpcServeNewApp(t, config)
	service := &grpcServerCaptureService{grpcServeService: grpcServeService{}}
	if err := app.AddGRPCServiceE(service); err != nil {
		t.Fatalf("AddGRPCServiceE() error = %v", err)
	}

	err = app.Serve(context.Background())
	if err == nil {
		t.Fatal("Serve() error = nil, want gRPC bind failure")
	}
	if service.server == nil {
		t.Fatal("gRPC service did not capture the built server")
	}
	assertCapturedGRPCServerStopped(t, service.server)
}

func assertCapturedGRPCServerStopped(t *testing.T, server *grpc.Server) {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	select {
	case serveErr := <-done:
		if !errors.Is(serveErr, grpc.ErrServerStopped) {
			t.Fatalf("captured gRPC Serve() error = %v, want ErrServerStopped", serveErr)
		}
	case <-time.After(150 * time.Millisecond):
		server.Stop()
		<-done
		t.Fatal("gRPC server remained usable after HTTP bind failure")
	}
}

type grpcServerCaptureService struct {
	grpcServeService
	server *grpc.Server
}

func (s *grpcServerCaptureService) Name() string { return "grpc-server-capture-service" }

func (s *grpcServerCaptureService) RegisterGRPC(registrar grpc.ServiceRegistrar) error {
	safe, ok := registrar.(*safeGRPCServiceRegistrar)
	if !ok {
		return errors.New("unexpected gRPC registrar type")
	}
	server, ok := safe.server.(*grpc.Server)
	if !ok {
		return errors.New("unexpected underlying gRPC server type")
	}
	s.server = server
	registrar.RegisterService(&grpcServeServiceDesc, s)
	return nil
}
