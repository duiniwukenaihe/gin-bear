package bear

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"log/slog"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

const (
	grpcSecurityServiceName  = "bear.test.SecurityEcho"
	grpcSecurityUnaryMethod  = "/bear.test.SecurityEcho/Echo"
	grpcSecurityStreamMethod = "/bear.test.SecurityEcho/EchoStream"
)

type grpcSecurityEchoServer interface {
	Echo(context.Context, *wrapperspb.BytesValue) (*wrapperspb.BytesValue, error)
	EchoStream(grpc.ServerStream) error
}

type grpcSecurityEchoService struct {
	panicUnary  atomic.Bool
	panicStream atomic.Bool
	unaryCalls  atomic.Int32
}

func (*grpcSecurityEchoService) Name() string { return "grpc-security-echo" }

func (s *grpcSecurityEchoService) RegisterGRPC(registrar grpc.ServiceRegistrar) error {
	registrar.RegisterService(&grpcSecurityServiceDesc, s)
	return nil
}

func (s *grpcSecurityEchoService) Echo(_ context.Context, request *wrapperspb.BytesValue) (*wrapperspb.BytesValue, error) {
	s.unaryCalls.Add(1)
	if s.panicUnary.Swap(false) {
		panic("grpc security unary panic must not escape")
	}
	return wrapperspb.Bytes(append([]byte(nil), request.Value...)), nil
}

func (s *grpcSecurityEchoService) EchoStream(stream grpc.ServerStream) error {
	if s.panicStream.Swap(false) {
		panic("grpc security stream panic must not escape")
	}
	request := new(wrapperspb.BytesValue)
	if err := stream.RecvMsg(request); err != nil {
		return err
	}
	return stream.SendMsg(wrapperspb.Bytes(append([]byte(nil), request.Value...)))
}

func grpcSecurityUnaryHandler(
	server any,
	ctx context.Context,
	decode func(any) error,
	interceptor grpc.UnaryServerInterceptor,
) (any, error) {
	request := new(wrapperspb.BytesValue)
	if err := decode(request); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return server.(grpcSecurityEchoServer).Echo(ctx, request)
	}
	info := &grpc.UnaryServerInfo{Server: server, FullMethod: grpcSecurityUnaryMethod}
	handler := func(ctx context.Context, request any) (any, error) {
		return server.(grpcSecurityEchoServer).Echo(ctx, request.(*wrapperspb.BytesValue))
	}
	return interceptor(ctx, request, info, handler)
}

func grpcSecurityStreamHandler(server any, stream grpc.ServerStream) error {
	return server.(grpcSecurityEchoServer).EchoStream(stream)
}

var grpcSecurityServiceDesc = grpc.ServiceDesc{
	ServiceName: grpcSecurityServiceName,
	HandlerType: (*grpcSecurityEchoServer)(nil),
	Methods: []grpc.MethodDesc{{
		MethodName: "Echo",
		Handler:    grpcSecurityUnaryHandler,
	}},
	Streams: []grpc.StreamDesc{{
		StreamName:    "EchoStream",
		Handler:       grpcSecurityStreamHandler,
		ServerStreams: true,
		ClientStreams: true,
	}},
}

func TestGRPCConfigDefaultsMatchProductionRuntimeDesign(t *testing.T) {
	config := NewSysConfig()
	if config.GRPC == nil {
		t.Fatal("NewSysConfig().GRPC = nil")
	}

	want := GRPCConfig{
		Enabled:               false,
		Host:                  "127.0.0.1",
		Port:                  9090,
		TransportSecurity:     "",
		TLSCertFile:           "",
		TLSKeyFile:            "",
		ClientCAFile:          "",
		MaxRecvMessageBytes:   4 << 20,
		MaxSendMessageBytes:   4 << 20,
		MaxConcurrentStreams:  128,
		KeepaliveMinTime:      "5m",
		KeepaliveTime:         "2h",
		KeepaliveTimeout:      "20s",
		MaxConnectionIdle:     "15m",
		MaxConnectionAge:      "2h",
		MaxConnectionAgeGrace: "5m",
		HealthEnabled:         true,
		ReflectionEnabled:     false,
	}
	if !reflect.DeepEqual(*config.GRPC, want) {
		t.Fatalf("NewSysConfig().GRPC = %#v, want %#v", *config.GRPC, want)
	}
	if got := grpcListenAddress(config.GRPC); got != "127.0.0.1:9090" {
		t.Fatalf("grpcListenAddress() = %q, want %q", got, "127.0.0.1:9090")
	}
}

func TestGRPCConfigDisabledIgnoresInvalidSecuritySettings(t *testing.T) {
	config := NewSysConfig()
	config.GRPC.Enabled = false
	config.GRPC.TransportSecurity = "unsupported"
	config.GRPC.MaxRecvMessageBytes = -1
	config.GRPC.KeepaliveTime = "not-a-duration"

	if err := validateGRPCConfig(config); err != nil {
		t.Fatalf("validateGRPCConfig() rejected disabled gRPC: %v", err)
	}
}

func TestGRPCConfigAcceptsExactResourceBoundaries(t *testing.T) {
	t.Setenv("BEAR_ENV", "dev")
	tests := []struct {
		name   string
		mutate func(*GRPCConfig)
	}{
		{name: "minimum receive message", mutate: func(c *GRPCConfig) { c.MaxRecvMessageBytes = 1 }},
		{name: "maximum receive message", mutate: func(c *GRPCConfig) { c.MaxRecvMessageBytes = 64 << 20 }},
		{name: "minimum send message", mutate: func(c *GRPCConfig) { c.MaxSendMessageBytes = 1 }},
		{name: "maximum send message", mutate: func(c *GRPCConfig) { c.MaxSendMessageBytes = 64 << 20 }},
		{name: "minimum concurrent streams", mutate: func(c *GRPCConfig) { c.MaxConcurrentStreams = 1 }},
		{name: "maximum concurrent streams", mutate: func(c *GRPCConfig) { c.MaxConcurrentStreams = 65535 }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := grpcSecurityPlaintextConfig()
			tt.mutate(config.GRPC)
			if err := validateGRPCConfig(config); err != nil {
				t.Fatalf("validateGRPCConfig() rejected exact boundary: %v", err)
			}
		})
	}
}

func TestGRPCConfigRejectsResourceValuesOutsideExactBoundaries(t *testing.T) {
	t.Setenv("BEAR_ENV", "dev")
	tests := []struct {
		name    string
		mutate  func(*GRPCConfig)
		wantErr string
	}{
		{name: "zero receive message", mutate: func(c *GRPCConfig) { c.MaxRecvMessageBytes = 0 }, wantErr: "max_recv_message_bytes"},
		{name: "negative receive message", mutate: func(c *GRPCConfig) { c.MaxRecvMessageBytes = -1 }, wantErr: "max_recv_message_bytes"},
		{name: "receive message above maximum", mutate: func(c *GRPCConfig) { c.MaxRecvMessageBytes = (64 << 20) + 1 }, wantErr: "max_recv_message_bytes"},
		{name: "zero send message", mutate: func(c *GRPCConfig) { c.MaxSendMessageBytes = 0 }, wantErr: "max_send_message_bytes"},
		{name: "negative send message", mutate: func(c *GRPCConfig) { c.MaxSendMessageBytes = -1 }, wantErr: "max_send_message_bytes"},
		{name: "send message above maximum", mutate: func(c *GRPCConfig) { c.MaxSendMessageBytes = (64 << 20) + 1 }, wantErr: "max_send_message_bytes"},
		{name: "zero concurrent streams", mutate: func(c *GRPCConfig) { c.MaxConcurrentStreams = 0 }, wantErr: "max_concurrent_streams"},
		{name: "concurrent streams above maximum", mutate: func(c *GRPCConfig) { c.MaxConcurrentStreams = 65536 }, wantErr: "max_concurrent_streams"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := grpcSecurityPlaintextConfig()
			tt.mutate(config.GRPC)
			grpcSecurityRequireConfigError(t, config, tt.wantErr)
		})
	}
}

func TestGRPCConfigAcceptsExactDurationBoundaries(t *testing.T) {
	t.Setenv("BEAR_ENV", "dev")
	tests := []struct {
		name   string
		mutate func(*GRPCConfig)
	}{
		{name: "keepalive min minimum", mutate: func(c *GRPCConfig) { c.KeepaliveMinTime = "1ns" }},
		{name: "keepalive min maximum", mutate: func(c *GRPCConfig) { c.KeepaliveMinTime = "24h" }},
		{name: "keepalive time minimum", mutate: func(c *GRPCConfig) { c.KeepaliveTime = "1ns" }},
		{name: "keepalive time maximum", mutate: func(c *GRPCConfig) { c.KeepaliveTime = "24h" }},
		{name: "keepalive timeout minimum", mutate: func(c *GRPCConfig) { c.KeepaliveTimeout = "1ns" }},
		{name: "keepalive timeout maximum", mutate: func(c *GRPCConfig) { c.KeepaliveTimeout = "5m" }},
		{name: "connection idle minimum", mutate: func(c *GRPCConfig) { c.MaxConnectionIdle = "1ns" }},
		{name: "connection idle maximum", mutate: func(c *GRPCConfig) { c.MaxConnectionIdle = "24h" }},
		{name: "connection age minimum", mutate: func(c *GRPCConfig) { c.MaxConnectionAge = "1ns" }},
		{name: "connection age maximum", mutate: func(c *GRPCConfig) { c.MaxConnectionAge = "24h" }},
		{name: "connection age grace minimum", mutate: func(c *GRPCConfig) { c.MaxConnectionAgeGrace = "1ns" }},
		{name: "connection age grace maximum", mutate: func(c *GRPCConfig) { c.MaxConnectionAgeGrace = "5m" }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := grpcSecurityPlaintextConfig()
			tt.mutate(config.GRPC)
			if err := validateGRPCConfig(config); err != nil {
				t.Fatalf("validateGRPCConfig() rejected exact duration boundary: %v", err)
			}
		})
	}
}

func TestGRPCConfigRejectsInvalidDurationBoundaries(t *testing.T) {
	t.Setenv("BEAR_ENV", "dev")
	tests := []struct {
		field   string
		mutate  func(*GRPCConfig, string)
		maximum string
	}{
		{field: "keepalive_min_time", mutate: func(c *GRPCConfig, value string) { c.KeepaliveMinTime = value }, maximum: "24h"},
		{field: "keepalive_time", mutate: func(c *GRPCConfig, value string) { c.KeepaliveTime = value }, maximum: "24h"},
		{field: "keepalive_timeout", mutate: func(c *GRPCConfig, value string) { c.KeepaliveTimeout = value }, maximum: "5m"},
		{field: "max_connection_idle", mutate: func(c *GRPCConfig, value string) { c.MaxConnectionIdle = value }, maximum: "24h"},
		{field: "max_connection_age", mutate: func(c *GRPCConfig, value string) { c.MaxConnectionAge = value }, maximum: "24h"},
		{field: "max_connection_age_grace", mutate: func(c *GRPCConfig, value string) { c.MaxConnectionAgeGrace = value }, maximum: "5m"},
	}

	for _, tt := range tests {
		for _, value := range []string{"", "0s", "-1ns", "not-a-duration", grpcSecurityDurationAbove(t, tt.maximum)} {
			t.Run(tt.field+"/"+value, func(t *testing.T) {
				config := grpcSecurityPlaintextConfig()
				tt.mutate(config.GRPC, value)
				grpcSecurityRequireConfigError(t, config, tt.field)
			})
		}
	}
}

func TestGRPCConfigTransportSecurityEnvironmentRules(t *testing.T) {
	tests := []struct {
		name        string
		environment string
		host        string
		transport   string
		wantErr     string
	}{
		{name: "development empty falls back", environment: "dev", host: "127.0.0.1", transport: ""},
		{name: "development plaintext", environment: "dev", host: "0.0.0.0", transport: "plaintext"},
		{name: "production empty rejected", environment: "production", host: "127.0.0.1", transport: "", wantErr: "transport_security"},
		{name: "production IPv4 loopback plaintext", environment: "production", host: "127.0.0.1", transport: "plaintext"},
		{name: "production IPv4 loopback range plaintext", environment: "production", host: "127.12.34.56", transport: "plaintext"},
		{name: "production IPv6 loopback plaintext", environment: "production", host: "::1", transport: "plaintext"},
		{name: "production localhost plaintext", environment: "production", host: "localhost", transport: "plaintext"},
		{name: "production wildcard plaintext rejected", environment: "production", host: "0.0.0.0", transport: "plaintext", wantErr: "loopback"},
		{name: "production IPv6 wildcard plaintext rejected", environment: "production", host: "::", transport: "plaintext", wantErr: "loopback"},
		{name: "production public plaintext rejected", environment: "production", host: "192.0.2.10", transport: "plaintext", wantErr: "loopback"},
		{name: "unsupported transport rejected", environment: "dev", host: "127.0.0.1", transport: "starttls", wantErr: "transport_security"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("BEAR_ENV", tt.environment)
			config := grpcSecurityPlaintextConfig()
			config.GRPC.Host = tt.host
			config.GRPC.TransportSecurity = tt.transport
			err := validateGRPCConfig(config)
			if tt.wantErr == "" && err != nil {
				t.Fatalf("validateGRPCConfig() = %v, want nil", err)
			}
			if tt.wantErr != "" && (err == nil || !strings.Contains(strings.ToLower(err.Error()), tt.wantErr)) {
				t.Fatalf("validateGRPCConfig() = %v, want error containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestGRPCConfigTLSAndMTLSRequireFileSettings(t *testing.T) {
	t.Setenv("BEAR_ENV", "dev")
	tests := []struct {
		name      string
		transport string
		cert      string
		key       string
		clientCA  string
		wantErr   string
	}{
		{name: "tls missing certificate", transport: "tls", key: "server.key", wantErr: "tls_cert_file"},
		{name: "tls missing key", transport: "tls", cert: "server.crt", wantErr: "tls_key_file"},
		{name: "mtls missing certificate", transport: "mtls", key: "server.key", clientCA: "client-ca.crt", wantErr: "tls_cert_file"},
		{name: "mtls missing key", transport: "mtls", cert: "server.crt", clientCA: "client-ca.crt", wantErr: "tls_key_file"},
		{name: "mtls missing client CA", transport: "mtls", cert: "server.crt", key: "server.key", wantErr: "client_ca_file"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := grpcSecurityPlaintextConfig()
			config.GRPC.TransportSecurity = tt.transport
			config.GRPC.TLSCertFile = tt.cert
			config.GRPC.TLSKeyFile = tt.key
			config.GRPC.ClientCAFile = tt.clientCA
			grpcSecurityRequireConfigError(t, config, tt.wantErr)
		})
	}
}

func TestGRPCConfigListenAddressJoinsIPv6HostAndPort(t *testing.T) {
	config := NewSysConfig().GRPC
	config.Host = "::1"
	config.Port = 9443
	if got := grpcListenAddress(config); got != "[::1]:9443" {
		t.Fatalf("grpcListenAddress() = %q, want %q", got, "[::1]:9443")
	}
}

func TestGRPCRuntimeRejectsEmptyBusinessServiceList(t *testing.T) {
	app := grpcSecurityNewApp(t, grpcSecurityPlaintextConfig())
	if _, err := app.buildGRPCRuntime(); err == nil || !strings.Contains(strings.ToLower(err.Error()), "business service") {
		t.Fatalf("buildGRPCRuntime() error = %v, want missing business service rejection", err)
	}
}

func TestGRPCRuntimeUnaryCallSucceeds(t *testing.T) {
	service := &grpcSecurityEchoService{}
	runtime := grpcSecurityBuildRuntime(t, grpcSecurityPlaintextConfig(), service)
	connection := grpcSecurityServeBufconn(t, runtime)

	response := new(wrapperspb.BytesValue)
	if err := connection.Invoke(context.Background(), grpcSecurityUnaryMethod, wrapperspb.Bytes([]byte("hello")), response); err != nil {
		t.Fatalf("Invoke() failed: %v", err)
	}
	if got := string(response.Value); got != "hello" {
		t.Fatalf("response = %q, want %q", got, "hello")
	}
}

func TestGRPCRuntimeRecoversUnaryPanicAndKeepsServing(t *testing.T) {
	service := &grpcSecurityEchoService{}
	service.panicUnary.Store(true)
	runtime := grpcSecurityBuildRuntime(t, grpcSecurityPlaintextConfig(), service)
	connection := grpcSecurityServeBufconn(t, runtime)

	err := connection.Invoke(context.Background(), grpcSecurityUnaryMethod, wrapperspb.Bytes([]byte("panic")), new(wrapperspb.BytesValue))
	if status.Code(err) != codes.Internal {
		t.Fatalf("panic RPC code = %v, want %v (error %v)", status.Code(err), codes.Internal, err)
	}
	response := new(wrapperspb.BytesValue)
	if err := connection.Invoke(context.Background(), grpcSecurityUnaryMethod, wrapperspb.Bytes([]byte("still-alive")), response); err != nil {
		t.Fatalf("RPC after panic failed: %v", err)
	}
	if got := string(response.Value); got != "still-alive" {
		t.Fatalf("response after panic = %q, want %q", got, "still-alive")
	}
}

func TestGRPCUnaryPanicLogsRequestIDAndInternalAccessStatus(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(&ContextHandler{Handler: slog.NewJSONHandler(&logs, nil)})
	interceptor := grpcUnaryRecoveryInterceptor(logger)
	observed := grpcUnaryObservabilityInterceptor(logger)
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("x-request-id", "grpc-panic-rid"))

	_, err := interceptor(ctx, nil, &grpc.UnaryServerInfo{FullMethod: grpcSecurityUnaryMethod}, func(ctx context.Context, request any) (any, error) {
		return observed(ctx, request, &grpc.UnaryServerInfo{FullMethod: grpcSecurityUnaryMethod}, func(context.Context, any) (any, error) {
			panic("grpc panic log test")
		})
	})
	if status.Code(err) != codes.Internal {
		t.Fatalf("panic error code = %s, want %s", status.Code(err), codes.Internal)
	}
	for _, want := range []string{"grpc-panic-rid", "gRPC panic recovered", "gRPC request handled", `"status":"Internal"`} {
		if !strings.Contains(logs.String(), want) {
			t.Fatalf("panic logs = %s, want %q", logs.String(), want)
		}
	}
}

func TestGRPCRuntimeRecoversStreamPanicAndKeepsServing(t *testing.T) {
	service := &grpcSecurityEchoService{}
	service.panicStream.Store(true)
	runtime := grpcSecurityBuildRuntime(t, grpcSecurityPlaintextConfig(), service)
	connection := grpcSecurityServeBufconn(t, runtime)

	stream, err := connection.NewStream(context.Background(), &grpcSecurityServiceDesc.Streams[0], grpcSecurityStreamMethod)
	if err != nil {
		t.Fatalf("NewStream() failed: %v", err)
	}
	if err := stream.SendMsg(wrapperspb.Bytes([]byte("panic"))); err != nil {
		t.Fatalf("SendMsg() failed: %v", err)
	}
	_ = stream.CloseSend()
	err = stream.RecvMsg(new(wrapperspb.BytesValue))
	if status.Code(err) != codes.Internal {
		t.Fatalf("stream panic code = %v, want %v (error %v)", status.Code(err), codes.Internal, err)
	}

	response := new(wrapperspb.BytesValue)
	if err := connection.Invoke(context.Background(), grpcSecurityUnaryMethod, wrapperspb.Bytes([]byte("still-alive")), response); err != nil {
		t.Fatalf("unary RPC after stream panic failed: %v", err)
	}
}

func TestGRPCRuntimeUserUnaryInterceptorsRunInRegistrationOrder(t *testing.T) {
	var order []string
	first := func(ctx context.Context, request any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		order = append(order, "first-before")
		response, err := handler(ctx, request)
		order = append(order, "first-after")
		return response, err
	}
	second := func(ctx context.Context, request any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		order = append(order, "second-before")
		response, err := handler(ctx, request)
		order = append(order, "second-after")
		return response, err
	}

	config := grpcSecurityPlaintextConfig()
	app := grpcSecurityNewApp(t, config)
	if err := app.AddGRPCUnaryInterceptorE(first, second); err != nil {
		t.Fatalf("AddGRPCUnaryInterceptorE() failed: %v", err)
	}
	service := &grpcSecurityEchoService{}
	grpcSecurityAddService(t, app, service)
	runtime, err := app.buildGRPCRuntime()
	if err != nil {
		t.Fatalf("buildGRPCRuntime() failed: %v", err)
	}
	connection := grpcSecurityServeBufconn(t, runtime)

	if err := connection.Invoke(context.Background(), grpcSecurityUnaryMethod, wrapperspb.Bytes([]byte("order")), new(wrapperspb.BytesValue)); err != nil {
		t.Fatalf("Invoke() failed: %v", err)
	}
	want := []string{"first-before", "second-before", "second-after", "first-after"}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("interceptor order = %v, want %v", order, want)
	}
}

func TestGRPCRuntimeUserUnaryInterceptorCanRejectBeforeHandler(t *testing.T) {
	reject := func(context.Context, any, *grpc.UnaryServerInfo, grpc.UnaryHandler) (any, error) {
		return nil, status.Error(codes.PermissionDenied, "denied by test interceptor")
	}
	config := grpcSecurityPlaintextConfig()
	app := grpcSecurityNewApp(t, config)
	if err := app.AddGRPCUnaryInterceptorE(reject); err != nil {
		t.Fatalf("AddGRPCUnaryInterceptorE() failed: %v", err)
	}
	service := &grpcSecurityEchoService{}
	grpcSecurityAddService(t, app, service)
	runtime, err := app.buildGRPCRuntime()
	if err != nil {
		t.Fatalf("buildGRPCRuntime() failed: %v", err)
	}
	connection := grpcSecurityServeBufconn(t, runtime)

	err = connection.Invoke(context.Background(), grpcSecurityUnaryMethod, wrapperspb.Bytes([]byte("secret")), new(wrapperspb.BytesValue))
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("rejected RPC code = %v, want %v (error %v)", status.Code(err), codes.PermissionDenied, err)
	}
	if calls := service.unaryCalls.Load(); calls != 0 {
		t.Fatalf("handler calls = %d, want 0", calls)
	}
}

func TestGRPCRuntimeEnforcesReceiveMessageSize(t *testing.T) {
	config := grpcSecurityPlaintextConfig()
	config.GRPC.MaxRecvMessageBytes = 64
	runtime := grpcSecurityBuildRuntime(t, config, &grpcSecurityEchoService{})
	connection := grpcSecurityServeBufconn(t, runtime)

	err := connection.Invoke(context.Background(), grpcSecurityUnaryMethod, wrapperspb.Bytes(make([]byte, 256)), new(wrapperspb.BytesValue))
	if status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("oversized receive code = %v, want %v (error %v)", status.Code(err), codes.ResourceExhausted, err)
	}
}

func TestGRPCRuntimeEnforcesSendMessageSize(t *testing.T) {
	config := grpcSecurityPlaintextConfig()
	config.GRPC.MaxSendMessageBytes = 64
	runtime := grpcSecurityBuildRuntime(t, config, &grpcSecurityEchoService{})
	connection := grpcSecurityServeBufconn(t, runtime)

	err := connection.Invoke(context.Background(), grpcSecurityUnaryMethod, wrapperspb.Bytes(make([]byte, 256)), new(wrapperspb.BytesValue))
	if status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("oversized send code = %v, want %v (error %v)", status.Code(err), codes.ResourceExhausted, err)
	}
}

func TestGRPCRuntimeHealthTracksServingAndShutdownStates(t *testing.T) {
	runtime := grpcSecurityBuildRuntime(t, grpcSecurityPlaintextConfig(), &grpcSecurityEchoService{})
	connection := grpcSecurityServeBufconn(t, runtime)
	client := healthpb.NewHealthClient(connection)

	grpcSecurityRequireHealth(t, client, "", healthpb.HealthCheckResponse_NOT_SERVING)
	grpcSecurityRequireHealth(t, client, grpcSecurityServiceName, healthpb.HealthCheckResponse_NOT_SERVING)
	runtime.setServing()
	grpcSecurityRequireHealth(t, client, "", healthpb.HealthCheckResponse_SERVING)
	grpcSecurityRequireHealth(t, client, grpcSecurityServiceName, healthpb.HealthCheckResponse_SERVING)
	runtime.setNotServing()
	grpcSecurityRequireHealth(t, client, "", healthpb.HealthCheckResponse_NOT_SERVING)
	grpcSecurityRequireHealth(t, client, grpcSecurityServiceName, healthpb.HealthCheckResponse_NOT_SERVING)
}

func TestGRPCRuntimeHealthCanBeDisabled(t *testing.T) {
	config := grpcSecurityPlaintextConfig()
	config.GRPC.HealthEnabled = false
	runtime := grpcSecurityBuildRuntime(t, config, &grpcSecurityEchoService{})
	connection := grpcSecurityServeBufconn(t, runtime)

	_, err := healthpb.NewHealthClient(connection).Check(context.Background(), &healthpb.HealthCheckRequest{})
	if status.Code(err) != codes.Unimplemented {
		t.Fatalf("disabled health code = %v, want %v (error %v)", status.Code(err), codes.Unimplemented, err)
	}
}

func TestGRPCRuntimeReflectionRequiresExplicitEnablement(t *testing.T) {
	for _, tt := range []struct {
		name    string
		enabled bool
		want    bool
	}{
		{name: "disabled by default", enabled: false, want: false},
		{name: "explicitly enabled", enabled: true, want: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			config := grpcSecurityPlaintextConfig()
			config.GRPC.ReflectionEnabled = tt.enabled
			runtime := grpcSecurityBuildRuntime(t, config, &grpcSecurityEchoService{})
			_, registered := runtime.GetServiceInfo()["grpc.reflection.v1.ServerReflection"]
			if registered != tt.want {
				t.Fatalf("reflection registered = %v, want %v", registered, tt.want)
			}
		})
	}
}

func TestGRPCTLSAcceptsTLSAndRejectsPlaintext(t *testing.T) {
	files := grpcSecurityGenerateCertificates(t)
	config := grpcSecurityPlaintextConfig()
	config.GRPC.TransportSecurity = "tls"
	config.GRPC.TLSCertFile = files.serverCert
	config.GRPC.TLSKeyFile = files.serverKey
	runtime := grpcSecurityBuildRuntime(t, config, &grpcSecurityEchoService{})
	address := grpcSecurityServeTCP(t, runtime)

	tlsCredentials := credentials.NewTLS(&tls.Config{
		MinVersion: tls.VersionTLS12,
		RootCAs:    files.roots,
		ServerName: "localhost",
	})
	connection := grpcSecurityDial(t, address, tlsCredentials)
	if err := connection.Invoke(context.Background(), grpcSecurityUnaryMethod, wrapperspb.Bytes([]byte("tls")), new(wrapperspb.BytesValue)); err != nil {
		t.Fatalf("TLS Invoke() failed: %v", err)
	}

	plaintext := grpcSecurityDial(t, address, insecure.NewCredentials())
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err := plaintext.Invoke(ctx, grpcSecurityUnaryMethod, wrapperspb.Bytes([]byte("plaintext")), new(wrapperspb.BytesValue))
	if err == nil {
		t.Fatal("plaintext client unexpectedly called TLS server")
	}
}

func TestGRPCMTLSRequiresTrustedClientCertificate(t *testing.T) {
	files := grpcSecurityGenerateCertificates(t)
	config := grpcSecurityPlaintextConfig()
	config.GRPC.TransportSecurity = "mtls"
	config.GRPC.TLSCertFile = files.serverCert
	config.GRPC.TLSKeyFile = files.serverKey
	config.GRPC.ClientCAFile = files.caCert
	runtime := grpcSecurityBuildRuntime(t, config, &grpcSecurityEchoService{})
	address := grpcSecurityServeTCP(t, runtime)

	trustedCredentials := credentials.NewTLS(&tls.Config{
		MinVersion:   tls.VersionTLS12,
		RootCAs:      files.roots,
		ServerName:   "localhost",
		Certificates: []tls.Certificate{files.clientCertificate},
	})
	trusted := grpcSecurityDial(t, address, trustedCredentials)
	if err := trusted.Invoke(context.Background(), grpcSecurityUnaryMethod, wrapperspb.Bytes([]byte("mtls")), new(wrapperspb.BytesValue)); err != nil {
		t.Fatalf("mTLS Invoke() failed: %v", err)
	}

	missingClientCertificate := credentials.NewTLS(&tls.Config{
		MinVersion: tls.VersionTLS12,
		RootCAs:    files.roots,
		ServerName: "localhost",
	})
	untrusted := grpcSecurityDial(t, address, missingClientCertificate)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err := untrusted.Invoke(ctx, grpcSecurityUnaryMethod, wrapperspb.Bytes([]byte("missing-client-cert")), new(wrapperspb.BytesValue))
	if err == nil {
		t.Fatal("client without certificate unexpectedly called mTLS server")
	}
}

func grpcSecurityPlaintextConfig() *SysConfig {
	config := NewSysConfig()
	config.Server.Mode = "debug"
	config.GRPC.Enabled = true
	config.GRPC.Host = "127.0.0.1"
	config.GRPC.TransportSecurity = "plaintext"
	return config
}

func grpcSecurityRequireConfigError(t *testing.T, config *SysConfig, want string) {
	t.Helper()
	err := validateGRPCConfig(config)
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), want) {
		t.Fatalf("validateGRPCConfig() = %v, want error containing %q", err, want)
	}
}

func grpcSecurityDurationAbove(t *testing.T, maximum string) string {
	t.Helper()
	duration, err := time.ParseDuration(maximum)
	if err != nil {
		t.Fatalf("ParseDuration(%q): %v", maximum, err)
	}
	return (duration + time.Nanosecond).String()
}

func grpcSecurityNewApp(t *testing.T, config *SysConfig) *Bear {
	t.Helper()
	app, err := IgniteE(config)
	if err != nil {
		t.Fatalf("IgniteE() failed: %v", err)
	}
	return app
}

func grpcSecurityAddService(t *testing.T, app *Bear, service *grpcSecurityEchoService) {
	t.Helper()
	if err := app.AddGRPCServiceE(service); err != nil {
		t.Fatalf("AddGRPCServiceE() failed: %v", err)
	}
}

func grpcSecurityBuildRuntime(t *testing.T, config *SysConfig, service *grpcSecurityEchoService) *grpcRuntimeServer {
	t.Helper()
	app := grpcSecurityNewApp(t, config)
	grpcSecurityAddService(t, app, service)
	runtime, err := app.buildGRPCRuntime()
	if err != nil {
		t.Fatalf("buildGRPCRuntime() failed: %v", err)
	}
	return runtime
}

func grpcSecurityServeBufconn(t *testing.T, runtime *grpcRuntimeServer) *grpc.ClientConn {
	t.Helper()
	listener := bufconn.Listen(1 << 20)
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		_ = runtime.Serve(listener)
	}()
	t.Cleanup(runtime.Stop)

	connection, err := grpc.NewClient(
		"passthrough:///bufconn",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return listener.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("grpc.NewClient() failed: %v", err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	return connection
}

func grpcSecurityServeTCP(t *testing.T, runtime *grpcRuntimeServer) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen() failed: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		_ = runtime.Serve(listener)
	}()
	t.Cleanup(runtime.Stop)
	return listener.Addr().String()
}

func grpcSecurityDial(t *testing.T, address string, transport credentials.TransportCredentials) *grpc.ClientConn {
	t.Helper()
	connection, err := grpc.NewClient(address, grpc.WithTransportCredentials(transport))
	if err != nil {
		t.Fatalf("grpc.NewClient(%q) failed: %v", address, err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	return connection
}

func grpcSecurityRequireHealth(
	t *testing.T,
	client healthpb.HealthClient,
	service string,
	want healthpb.HealthCheckResponse_ServingStatus,
) {
	t.Helper()
	response, err := client.Check(context.Background(), &healthpb.HealthCheckRequest{Service: service})
	if err != nil {
		t.Fatalf("Health.Check(%q) failed: %v", service, err)
	}
	if response.Status != want {
		t.Fatalf("Health.Check(%q) = %v, want %v", service, response.Status, want)
	}
}

type grpcSecurityCertificateFiles struct {
	caCert            string
	serverCert        string
	serverKey         string
	roots             *x509.CertPool
	clientCertificate tls.Certificate
}

func grpcSecurityGenerateCertificates(t *testing.T) grpcSecurityCertificateFiles {
	t.Helper()
	directory := t.TempDir()
	now := time.Now()

	caKey := grpcSecurityGenerateKey(t)
	caTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "gin-bear test CA"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	caDER := grpcSecurityCreateCertificate(t, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	ca := grpcSecurityParseCertificate(t, caDER)
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})
	caCertPath := filepath.Join(directory, "ca.crt")
	grpcSecurityWriteFile(t, caCertPath, caPEM)

	serverCertPath, serverKeyPath, _ := grpcSecurityIssueCertificate(t, directory, "server", 2, ca, caKey, []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth})
	_, _, clientCertificate := grpcSecurityIssueCertificate(t, directory, "client", 3, ca, caKey, []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth})

	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caPEM) {
		t.Fatal("failed to append generated CA certificate")
	}
	return grpcSecurityCertificateFiles{
		caCert:            caCertPath,
		serverCert:        serverCertPath,
		serverKey:         serverKeyPath,
		roots:             roots,
		clientCertificate: clientCertificate,
	}
}

func grpcSecurityIssueCertificate(
	t *testing.T,
	directory string,
	name string,
	serial int64,
	ca *x509.Certificate,
	caKey *ecdsa.PrivateKey,
	usage []x509.ExtKeyUsage,
) (string, string, tls.Certificate) {
	t.Helper()
	key := grpcSecurityGenerateKey(t)
	template := &x509.Certificate{
		SerialNumber: big.NewInt(serial),
		Subject:      pkix.Name{CommonName: name},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  usage,
	}
	if name == "server" {
		template.DNSNames = []string{"localhost"}
		template.IPAddresses = []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")}
	}
	certificateDER := grpcSecurityCreateCertificate(t, template, ca, &key.PublicKey, caKey)
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("MarshalPKCS8PrivateKey() failed: %v", err)
	}
	certificatePEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificateDER})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	certificatePath := filepath.Join(directory, name+".crt")
	keyPath := filepath.Join(directory, name+".key")
	grpcSecurityWriteFile(t, certificatePath, certificatePEM)
	grpcSecurityWriteFile(t, keyPath, keyPEM)
	pair, err := tls.X509KeyPair(certificatePEM, keyPEM)
	if err != nil {
		t.Fatalf("tls.X509KeyPair() failed: %v", err)
	}
	return certificatePath, keyPath, pair
}

func grpcSecurityGenerateKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("ecdsa.GenerateKey() failed: %v", err)
	}
	return key
}

func grpcSecurityCreateCertificate(
	t *testing.T,
	template *x509.Certificate,
	parent *x509.Certificate,
	publicKey any,
	privateKey any,
) []byte {
	t.Helper()
	der, err := x509.CreateCertificate(rand.Reader, template, parent, publicKey, privateKey)
	if err != nil {
		t.Fatalf("x509.CreateCertificate() failed: %v", err)
	}
	return der
}

func grpcSecurityParseCertificate(t *testing.T, der []byte) *x509.Certificate {
	t.Helper()
	certificate, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("x509.ParseCertificate() failed: %v", err)
	}
	return certificate
}

func grpcSecurityWriteFile(t *testing.T, path string, contents []byte) {
	t.Helper()
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatalf("os.WriteFile(%q) failed: %v", path, err)
	}
}
