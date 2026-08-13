package bear

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

const (
	grpcServeServiceName      = "bear.serve.test.v1.BlockingService"
	grpcServeUnaryFullMethod  = "/" + grpcServeServiceName + "/Unary"
	grpcServeStreamFullMethod = "/" + grpcServeServiceName + "/Stream"
)

type grpcServeBlockingService interface {
	Unary(context.Context, *wrapperspb.StringValue) (*wrapperspb.StringValue, error)
	Stream(*wrapperspb.StringValue, grpc.ServerStream) error
}

type grpcServeService struct {
	name         string
	registerErr  error
	entered      chan struct{}
	finished     chan struct{}
	release      <-chan struct{}
	ignoreCancel bool
	enterOnce    sync.Once
	finishOnce   sync.Once
}

func (s *grpcServeService) Name() string {
	if s.name != "" {
		return s.name
	}
	return "grpc-serve-blocking-service"
}

func (s *grpcServeService) RegisterGRPC(registrar grpc.ServiceRegistrar) error {
	if s.registerErr != nil {
		return s.registerErr
	}
	registrar.RegisterService(&grpcServeServiceDesc, s)
	return nil
}

func (s *grpcServeService) Unary(ctx context.Context, request *wrapperspb.StringValue) (*wrapperspb.StringValue, error) {
	if s.entered != nil {
		s.enterOnce.Do(func() { close(s.entered) })
	}
	if s.finished != nil {
		defer s.finishOnce.Do(func() { close(s.finished) })
	}
	if s.release != nil {
		if s.ignoreCancel {
			<-s.release
		} else {
			select {
			case <-s.release:
			case <-ctx.Done():
				return nil, status.FromContextError(ctx.Err()).Err()
			}
		}
	}
	return wrapperspb.String("reply:" + request.GetValue()), nil
}

func (s *grpcServeService) Stream(request *wrapperspb.StringValue, stream grpc.ServerStream) error {
	if s.entered != nil {
		s.enterOnce.Do(func() { close(s.entered) })
	}
	if s.finished != nil {
		defer s.finishOnce.Do(func() { close(s.finished) })
	}
	if s.release != nil {
		if s.ignoreCancel {
			<-s.release
		} else {
			select {
			case <-s.release:
			case <-stream.Context().Done():
				return status.FromContextError(stream.Context().Err()).Err()
			}
		}
	}
	return stream.SendMsg(wrapperspb.String("reply:" + request.GetValue()))
}

var grpcServeServiceDesc = grpc.ServiceDesc{
	ServiceName: grpcServeServiceName,
	HandlerType: (*grpcServeBlockingService)(nil),
	Methods: []grpc.MethodDesc{
		{
			MethodName: "Unary",
			Handler:    grpcServeUnaryHandler,
		},
	},
	Streams: []grpc.StreamDesc{
		{
			StreamName:    "Stream",
			Handler:       grpcServeStreamHandler,
			ServerStreams: true,
		},
	},
}

func grpcServeUnaryHandler(
	service any,
	ctx context.Context,
	decode func(any) error,
	interceptor grpc.UnaryServerInterceptor,
) (any, error) {
	request := new(wrapperspb.StringValue)
	if err := decode(request); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return service.(grpcServeBlockingService).Unary(ctx, request)
	}
	info := &grpc.UnaryServerInfo{Server: service, FullMethod: grpcServeUnaryFullMethod}
	handler := func(ctx context.Context, request any) (any, error) {
		return service.(grpcServeBlockingService).Unary(ctx, request.(*wrapperspb.StringValue))
	}
	return interceptor(ctx, request, info, handler)
}

func grpcServeStreamHandler(service any, stream grpc.ServerStream) error {
	request := new(wrapperspb.StringValue)
	if err := stream.RecvMsg(request); err != nil {
		return err
	}
	return service.(grpcServeBlockingService).Stream(request, stream)
}

type grpcServeLifecycleProbe struct {
	name              string
	started           chan struct{}
	stopping          chan struct{}
	blockUntilContext bool
	startOnce         sync.Once
	stopOnce          sync.Once
}

func (p *grpcServeLifecycleProbe) Name() string { return p.name }

func (p *grpcServeLifecycleProbe) Init(context.Context) error {
	p.startOnce.Do(func() { close(p.started) })
	return nil
}

func (p *grpcServeLifecycleProbe) ShutdownContext(ctx context.Context) error {
	p.stopOnce.Do(func() { close(p.stopping) })
	if !p.blockUntilContext {
		return nil
	}
	<-ctx.Done()
	return ctx.Err()
}

func TestServeGRPCPreflightRejectsInvalidTLSWithoutListeners(t *testing.T) {
	httpPort, grpcPort := grpcServeUnusedPorts(t)
	config := grpcServeConfig(httpPort, grpcPort, time.Second)
	app := grpcServeNewApp(t, config)
	grpcServeAddService(t, app, &grpcServeService{})

	config.GRPC.TransportSecurity = "tls"
	config.GRPC.TLSCertFile = t.TempDir() + "/missing-cert.pem"
	config.GRPC.TLSKeyFile = t.TempDir() + "/missing-key.pem"

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err := app.Serve(ctx)
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "tls") {
		t.Errorf("Serve() error = %v, want TLS preflight failure", err)
	}
	grpcServeAssertPortsReusable(t, httpPort, grpcPort)
}

func TestServeGRPCPreflightRejectsMissingBusinessServiceWithoutListeners(t *testing.T) {
	httpPort, grpcPort := grpcServeUnusedPorts(t)
	config := grpcServeConfig(httpPort, grpcPort, 300*time.Millisecond)
	app := grpcServeNewApp(t, config)

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	err := app.Serve(ctx)
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "service") {
		t.Errorf("Serve() error = %v, want missing business service preflight failure", err)
	}
	grpcServeAssertPortsReusable(t, httpPort, grpcPort)
}

func TestServeGRPCPreflightRejectsRegistrationErrorWithoutListeners(t *testing.T) {
	httpPort, grpcPort := grpcServeUnusedPorts(t)
	config := grpcServeConfig(httpPort, grpcPort, time.Second)
	app := grpcServeNewApp(t, config)
	registrationErr := errors.New("grpc serve registration rejected")
	grpcServeAddService(t, app, &grpcServeService{registerErr: registrationErr})

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err := app.Serve(ctx)
	if !errors.Is(err, registrationErr) {
		t.Errorf("Serve() error = %v, want registration error", err)
	}
	grpcServeAssertPortsReusable(t, httpPort, grpcPort)
}

func TestServeGRPCBindHTTPFailureDoesNotStartGRPC(t *testing.T) {
	httpBlocker, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	defer httpBlocker.Close()
	httpPort := httpBlocker.Addr().(*net.TCPAddr).Port
	_, grpcPort := grpcServeUnusedPorts(t)
	config := grpcServeConfig(httpPort, grpcPort, time.Second)
	app := grpcServeNewApp(t, config)
	grpcServeAddService(t, app, &grpcServeService{})

	err = app.Serve(context.Background())
	if err == nil || !strings.Contains(err.Error(), "HTTP") {
		t.Fatalf("Serve() error = %v, want HTTP bind failure", err)
	}
	grpcServeAssertPortReusable(t, grpcPort)
}

func TestServeGRPCBindFailureClosesHTTPAndLifecycle(t *testing.T) {
	grpcBlocker, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer grpcBlocker.Close()
	grpcPort := grpcBlocker.Addr().(*net.TCPAddr).Port
	httpPort, _ := grpcServeUnusedPorts(t)
	config := grpcServeConfig(httpPort, grpcPort, time.Second)
	app := grpcServeNewApp(t, config)
	grpcServeAddService(t, app, &grpcServeService{})
	probe := grpcServeNewLifecycleProbe("grpc-serve-bind-failure", false)
	grpcServeAddBean(t, app, probe)

	err = app.Serve(context.Background())
	if err == nil || !strings.Contains(err.Error(), "gRPC") {
		t.Errorf("Serve() error = %v, want gRPC bind failure", err)
	}
	grpcServeAssertPortReusable(t, httpPort)
	grpcServeAssertClosed(t, probe.started, "Lifecycle did not start before bind")
	grpcServeAssertClosed(t, probe.stopping, "Lifecycle was not cleaned up after gRPC bind failure")
}

func TestServeGRPCGracefulCompletesBlockingUnaryWithinDrainBudget(t *testing.T) {
	const shutdownBudget = 800 * time.Millisecond
	release := make(chan struct{})
	service := grpcServeNewBlockingService(release)
	probe := grpcServeNewLifecycleProbe("grpc-serve-graceful", false)
	running := grpcServeStart(t, service, probe, shutdownBudget)

	rpcDone := grpcServeInvokeUnary(running.conn)
	grpcServeWait(t, service.entered, "unary RPC did not enter")
	shutdownStarted := time.Now()
	running.cancel()
	grpcServeExpectHealth(t, running.healthWatch, healthpb.HealthCheckResponse_NOT_SERVING)
	running.cancelHealthWatch()

	grpcServeAssertOpen(t, probe.stopping, "Lifecycle stopped while unary RPC was draining")
	close(release)
	result := grpcServeWaitRPC(t, rpcDone, shutdownBudget)
	if result.err != nil {
		t.Fatalf("blocking unary error = %v", result.err)
	}
	if got := result.response.GetValue(); got != "reply:request" {
		t.Fatalf("blocking unary response = %q, want %q", got, "reply:request")
	}
	grpcServeWait(t, service.finished, "unary handler did not finish")
	grpcServeWait(t, probe.stopping, "Lifecycle did not stop after unary drain")
	if err := grpcServeWaitServe(t, running.done, shutdownBudget); err != nil {
		t.Fatalf("Serve() graceful shutdown error = %v", err)
	}
	if elapsed := time.Since(shutdownStarted); elapsed > shutdownBudget+250*time.Millisecond {
		t.Fatalf("graceful shutdown took %s, want at most %s", elapsed, shutdownBudget+250*time.Millisecond)
	}
}

func TestServeGRPCGracefulForcesNeverEndingUnaryAtDrainDeadline(t *testing.T) {
	const shutdownBudget = 800 * time.Millisecond
	neverRelease := make(chan struct{})
	service := grpcServeNewBlockingService(neverRelease)
	probe := grpcServeNewLifecycleProbe("grpc-serve-force", false)
	running := grpcServeStart(t, service, probe, shutdownBudget)

	rpcDone := grpcServeInvokeUnary(running.conn)
	grpcServeWait(t, service.entered, "never-ending unary RPC did not enter")
	shutdownStarted := time.Now()
	running.cancel()
	grpcServeExpectHealth(t, running.healthWatch, healthpb.HealthCheckResponse_NOT_SERVING)
	running.cancelHealthWatch()

	result := grpcServeWaitRPC(t, rpcDone, shutdownBudget)
	forcedAfter := time.Since(shutdownStarted)
	if result.err == nil {
		t.Fatal("forced unary unexpectedly completed without an error")
	}
	if code := status.Code(result.err); code != codes.Unavailable && code != codes.Canceled {
		t.Fatalf("forced unary code = %s, want Unavailable or Canceled", code)
	}
	grpcServeWait(t, service.finished, "forced unary handler did not finish")
	grpcServeWait(t, probe.stopping, "Lifecycle did not stop after forced unary")
	if forcedAfter < 500*time.Millisecond || forcedAfter > 750*time.Millisecond {
		t.Fatalf("unary was forced after %s, want force near the 75%% drain deadline", forcedAfter)
	}
	if err := grpcServeWaitServe(t, running.done, 400*time.Millisecond); err == nil {
		t.Fatal("Serve() error = nil, want graceful-drain deadline error")
	}
	if elapsed := time.Since(shutdownStarted); elapsed > shutdownBudget+250*time.Millisecond {
		t.Fatalf("forced shutdown took %s, want at most %s", elapsed, shutdownBudget+250*time.Millisecond)
	}
}

func TestServeUsesSingleShutdownBudget(t *testing.T) {
	const shutdownBudget = 500 * time.Millisecond
	neverRelease := make(chan struct{})
	service := grpcServeNewBlockingService(neverRelease)
	probe := grpcServeNewLifecycleProbe("grpc-serve-single-budget", true)
	running := grpcServeStart(t, service, probe, shutdownBudget)

	rpcDone := grpcServeInvokeUnary(running.conn)
	grpcServeWait(t, service.entered, "single-budget unary RPC did not enter")
	shutdownStarted := time.Now()
	running.cancel()
	grpcServeExpectHealth(t, running.healthWatch, healthpb.HealthCheckResponse_NOT_SERVING)
	running.cancelHealthWatch()

	result := grpcServeWaitRPC(t, rpcDone, shutdownBudget)
	if result.err == nil {
		t.Fatal("single-budget unary unexpectedly completed without an error")
	}
	grpcServeWait(t, service.finished, "single-budget unary handler did not finish")
	grpcServeWait(t, probe.stopping, "Lifecycle did not start cleanup after request drain")
	if err := grpcServeWaitServe(t, running.done, shutdownBudget); err == nil {
		t.Fatal("Serve() error = nil, want shutdown deadline errors")
	}
	if elapsed := time.Since(shutdownStarted); elapsed > shutdownBudget+250*time.Millisecond {
		t.Fatalf("shutdown took %s, want one total budget of at most %s", elapsed, shutdownBudget+250*time.Millisecond)
	}
}

func TestServeDoesNotCloseLifecycleResourcesWhileGRPCHandlerIgnoresCancellation(t *testing.T) {
	const shutdownBudget = 400 * time.Millisecond
	release := make(chan struct{})
	service := grpcServeNewBlockingService(release)
	service.ignoreCancel = true
	probe := grpcServeNewLifecycleProbe("grpc-serve-non-cooperative", false)
	running := grpcServeStart(t, service, probe, shutdownBudget)

	rpcDone := grpcServeInvokeUnary(running.conn)
	grpcServeWait(t, service.entered, "non-cooperative unary RPC did not enter")
	shutdownStarted := time.Now()
	running.cancel()
	grpcServeExpectHealth(t, running.healthWatch, healthpb.HealthCheckResponse_NOT_SERVING)
	running.cancelHealthWatch()

	result := grpcServeWaitRPC(t, rpcDone, shutdownBudget)
	if result.err == nil {
		t.Fatal("non-cooperative unary unexpectedly completed without an error")
	}
	serveErr := grpcServeWaitServe(t, running.done, shutdownBudget+250*time.Millisecond)
	if serveErr == nil || !strings.Contains(serveErr.Error(), "active gRPC handlers") {
		t.Fatalf("Serve() error = %v, want active gRPC handler shutdown error", serveErr)
	}
	grpcServeAssertOpen(t, service.finished, "non-cooperative handler unexpectedly finished")
	grpcServeAssertOpen(t, probe.stopping, "Lifecycle resources closed while a gRPC handler was still active")
	if elapsed := time.Since(shutdownStarted); elapsed > shutdownBudget+250*time.Millisecond {
		t.Fatalf("shutdown took %s, want at most %s", elapsed, shutdownBudget+250*time.Millisecond)
	}
	deferredShutdownStarted := time.Now()
	shutdownErr := running.app.Shutdown(context.Background())
	if shutdownErr == nil || !strings.Contains(shutdownErr.Error(), "active gRPC handlers") {
		t.Fatalf("deferred Shutdown() error = %v, want active gRPC handler error", shutdownErr)
	}
	if elapsed := time.Since(deferredShutdownStarted); elapsed > 100*time.Millisecond {
		t.Fatalf("deferred Shutdown() took %s, want a fast failure after handler timeout", elapsed)
	}
	grpcServeAssertOpen(t, probe.stopping, "Deferred Shutdown closed resources while a gRPC handler was still active")

	close(release)
	grpcServeWait(t, service.finished, "non-cooperative handler did not finish after release")
	if err := running.app.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown() after handler release error = %v", err)
	}
	grpcServeWait(t, probe.stopping, "Lifecycle resources did not close after handler release")
}

func TestServeDoesNotCloseLifecycleResourcesWhileGRPCStreamIgnoresCancellation(t *testing.T) {
	const shutdownBudget = 400 * time.Millisecond
	release := make(chan struct{})
	service := grpcServeNewBlockingService(release)
	service.ignoreCancel = true
	probe := grpcServeNewLifecycleProbe("grpc-serve-non-cooperative-stream", false)
	running := grpcServeStart(t, service, probe, shutdownBudget)

	streamDone := grpcServeInvokeStream(running.conn)
	grpcServeWait(t, service.entered, "non-cooperative stream RPC did not enter")
	shutdownStarted := time.Now()
	running.cancel()
	grpcServeExpectHealth(t, running.healthWatch, healthpb.HealthCheckResponse_NOT_SERVING)
	running.cancelHealthWatch()

	if err := grpcServeWaitStream(t, streamDone, shutdownBudget); err == nil {
		t.Fatal("non-cooperative stream unexpectedly completed without an error")
	}
	serveErr := grpcServeWaitServe(t, running.done, shutdownBudget+250*time.Millisecond)
	if serveErr == nil || !strings.Contains(serveErr.Error(), "active gRPC handlers") {
		t.Fatalf("Serve() error = %v, want active gRPC handler shutdown error", serveErr)
	}
	grpcServeAssertOpen(t, service.finished, "non-cooperative stream handler unexpectedly finished")
	grpcServeAssertOpen(t, probe.stopping, "Lifecycle resources closed while a gRPC stream handler was still active")
	if elapsed := time.Since(shutdownStarted); elapsed > shutdownBudget+250*time.Millisecond {
		t.Fatalf("shutdown took %s, want at most %s", elapsed, shutdownBudget+250*time.Millisecond)
	}
	deferredShutdownStarted := time.Now()
	shutdownErr := running.app.Shutdown(context.Background())
	if shutdownErr == nil || !strings.Contains(shutdownErr.Error(), "active gRPC handlers") {
		t.Fatalf("deferred Shutdown() error = %v, want active gRPC handler error", shutdownErr)
	}
	if elapsed := time.Since(deferredShutdownStarted); elapsed > 100*time.Millisecond {
		t.Fatalf("deferred Shutdown() took %s, want a fast failure after handler timeout", elapsed)
	}
	grpcServeAssertOpen(t, probe.stopping, "Deferred Shutdown closed resources while a gRPC stream handler was still active")

	close(release)
	grpcServeWait(t, service.finished, "non-cooperative stream handler did not finish after release")
	if err := running.app.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown() after stream handler release error = %v", err)
	}
	grpcServeWait(t, probe.stopping, "Lifecycle resources did not close after stream handler release")
}

type grpcServeRunningApp struct {
	app               *Bear
	cancel            context.CancelFunc
	done              <-chan error
	completed         <-chan struct{}
	conn              *grpc.ClientConn
	healthWatch       healthpb.Health_WatchClient
	cancelHealthWatch context.CancelFunc
}

func grpcServeStart(
	t *testing.T,
	service *grpcServeService,
	probe *grpcServeLifecycleProbe,
	shutdownBudget time.Duration,
) *grpcServeRunningApp {
	t.Helper()
	httpPort, grpcPort := grpcServeUnusedPorts(t)
	config := grpcServeConfig(httpPort, grpcPort, shutdownBudget)
	app := grpcServeNewApp(t, config)
	grpcServeAddService(t, app, service)
	grpcServeAddBean(t, app, probe)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	completed := make(chan struct{})
	go func() {
		done <- app.Serve(ctx)
		close(completed)
	}()

	conn, err := grpc.NewClient(
		fmt.Sprintf("127.0.0.1:%d", grpcPort),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	healthCtx, cancelHealth := context.WithTimeout(context.Background(), 3*time.Second)
	healthWatch, err := healthpb.NewHealthClient(conn).Watch(
		healthCtx,
		&healthpb.HealthCheckRequest{},
		grpc.WaitForReady(true),
	)
	if err != nil {
		cancelHealth()
		_ = conn.Close()
		cancel()
		t.Fatalf("Health.Watch() error = %v", err)
	}
	grpcServeExpectHealth(t, healthWatch, healthpb.HealthCheckResponse_SERVING)

	httpConn, err := net.DialTimeout("tcp4", fmt.Sprintf("127.0.0.1:%d", httpPort), 250*time.Millisecond)
	if err != nil {
		cancelHealth()
		_ = conn.Close()
		cancel()
		t.Fatalf("HTTP loopback listener is not reachable: %v", err)
	}
	_ = httpConn.Close()

	running := &grpcServeRunningApp{
		app:               app,
		cancel:            cancel,
		done:              done,
		completed:         completed,
		conn:              conn,
		healthWatch:       healthWatch,
		cancelHealthWatch: cancelHealth,
	}
	t.Cleanup(func() {
		cancelHealth()
		cancel()
		_ = conn.Close()
		select {
		case <-completed:
		case <-time.After(2 * time.Second):
		}
	})
	return running
}

type grpcServeRPCResult struct {
	response *wrapperspb.StringValue
	err      error
}

func grpcServeInvokeUnary(conn *grpc.ClientConn) <-chan grpcServeRPCResult {
	done := make(chan grpcServeRPCResult, 1)
	go func() {
		response := new(wrapperspb.StringValue)
		err := conn.Invoke(
			context.Background(),
			grpcServeUnaryFullMethod,
			wrapperspb.String("request"),
			response,
		)
		done <- grpcServeRPCResult{response: response, err: err}
	}()
	return done
}

func grpcServeInvokeStream(conn *grpc.ClientConn) <-chan error {
	done := make(chan error, 1)
	go func() {
		stream, err := conn.NewStream(
			context.Background(),
			&grpc.StreamDesc{ServerStreams: true},
			grpcServeStreamFullMethod,
		)
		if err != nil {
			done <- err
			return
		}
		if err := stream.SendMsg(wrapperspb.String("request")); err != nil {
			done <- err
			return
		}
		if err := stream.CloseSend(); err != nil {
			done <- err
			return
		}
		response := new(wrapperspb.StringValue)
		done <- stream.RecvMsg(response)
	}()
	return done
}

func grpcServeExpectHealth(
	t *testing.T,
	watch healthpb.Health_WatchClient,
	want healthpb.HealthCheckResponse_ServingStatus,
) {
	t.Helper()
	type result struct {
		response *healthpb.HealthCheckResponse
		err      error
	}
	done := make(chan result, 1)
	go func() {
		response, err := watch.Recv()
		done <- result{response: response, err: err}
	}()
	select {
	case got := <-done:
		if got.err != nil {
			t.Fatalf("Health.Watch Recv() error = %v, want status %s", got.err, want)
		}
		if got.response.GetStatus() != want {
			t.Fatalf("Health status = %s, want %s", got.response.GetStatus(), want)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for Health status %s", want)
	}
}

func grpcServeNewBlockingService(release <-chan struct{}) *grpcServeService {
	return &grpcServeService{
		entered:  make(chan struct{}),
		finished: make(chan struct{}),
		release:  release,
	}
}

func grpcServeNewLifecycleProbe(name string, block bool) *grpcServeLifecycleProbe {
	return &grpcServeLifecycleProbe{
		name:              name,
		started:           make(chan struct{}),
		stopping:          make(chan struct{}),
		blockUntilContext: block,
	}
}

func grpcServeConfig(httpPort, grpcPort int, shutdownBudget time.Duration) *SysConfig {
	config := NewSysConfig()
	config.SetFrameworkStrict(true)
	config.Server.Port = int32(httpPort)
	config.Server.ShutdownTimeout = shutdownBudget.String()
	config.GRPC.Enabled = true
	config.GRPC.Host = "127.0.0.1"
	config.GRPC.Port = int32(grpcPort)
	config.GRPC.TransportSecurity = "plaintext"
	config.GRPC.HealthEnabled = true
	return config
}

func grpcServeNewApp(t *testing.T, config *SysConfig) *Bear {
	t.Helper()
	app, err := IgniteE(config)
	if err != nil {
		t.Fatalf("IgniteE() error = %v", err)
	}
	return app
}

func grpcServeAddService(t *testing.T, app *Bear, service *grpcServeService) {
	t.Helper()
	if err := app.AddGRPCServiceE(service); err != nil {
		t.Fatalf("AddGRPCServiceE() error = %v", err)
	}
}

func grpcServeAddBean(t *testing.T, app *Bear, bean Bean) {
	t.Helper()
	if err := app.BeansE(bean); err != nil {
		t.Fatalf("BeansE() error = %v", err)
	}
}

func grpcServeUnusedPorts(t *testing.T) (int, int) {
	t.Helper()
	first, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	return first.Addr().(*net.TCPAddr).Port, second.Addr().(*net.TCPAddr).Port
}

func grpcServeAssertPortsReusable(t *testing.T, ports ...int) {
	t.Helper()
	for _, port := range ports {
		grpcServeAssertPortReusable(t, port)
	}
}

func grpcServeAssertPortReusable(t *testing.T, port int) {
	t.Helper()
	listener, err := net.Listen("tcp4", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatalf("loopback port %d still has a listener: %v", port, err)
	}
	if err := listener.Close(); err != nil {
		t.Fatalf("close loopback port %d probe: %v", port, err)
	}
}

func grpcServeWait(t *testing.T, event <-chan struct{}, message string) {
	t.Helper()
	select {
	case <-event:
	case <-time.After(2 * time.Second):
		t.Fatal(message)
	}
}

func grpcServeAssertClosed(t *testing.T, event <-chan struct{}, message string) {
	t.Helper()
	select {
	case <-event:
	default:
		t.Fatal(message)
	}
}

func grpcServeAssertOpen(t *testing.T, event <-chan struct{}, message string) {
	t.Helper()
	select {
	case <-event:
		t.Fatal(message)
	default:
	}
}

func grpcServeWaitRPC(t *testing.T, done <-chan grpcServeRPCResult, timeout time.Duration) grpcServeRPCResult {
	t.Helper()
	select {
	case result := <-done:
		return result
	case <-time.After(timeout):
		t.Fatal("timed out waiting for unary RPC")
		return grpcServeRPCResult{}
	}
}

func grpcServeWaitStream(t *testing.T, done <-chan error, timeout time.Duration) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(timeout):
		t.Fatal("timed out waiting for stream RPC")
		return nil
	}
}

func grpcServeWaitServe(t *testing.T, done <-chan error, timeout time.Duration) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(timeout):
		t.Fatal("timed out waiting for Serve to return")
		return nil
	}
}
