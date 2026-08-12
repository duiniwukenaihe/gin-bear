package bear

import (
	"context"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sort"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/reflection"
	"google.golang.org/grpc/status"
)

type grpcRuntimeServer struct {
	*grpc.Server
	health       *health.Server
	serviceNames []string
}

func (b *Bear) buildGRPCRuntime() (*grpcRuntimeServer, error) {
	if b == nil || b.runtime == nil || b.runtime.Config == nil {
		return nil, fmt.Errorf("bear runtime is unavailable")
	}
	config := b.runtime.Config
	if err := validateGRPCConfig(config); err != nil {
		return nil, err
	}
	if config.GRPC == nil || !config.GRPC.Enabled {
		return nil, nil
	}
	transportCredentials, err := grpcTransportCredentials(config)
	if err != nil {
		return nil, err
	}
	services, unaryInterceptors, streamInterceptors := b.grpcRegistrationSnapshot()
	if len(services) == 0 {
		return nil, fmt.Errorf("gRPC requires at least one business service")
	}

	grpcConfig := config.GRPC
	minTime, _ := time.ParseDuration(grpcConfig.KeepaliveMinTime)
	keepaliveTime, _ := time.ParseDuration(grpcConfig.KeepaliveTime)
	keepaliveTimeout, _ := time.ParseDuration(grpcConfig.KeepaliveTimeout)
	maxIdle, _ := time.ParseDuration(grpcConfig.MaxConnectionIdle)
	maxAge, _ := time.ParseDuration(grpcConfig.MaxConnectionAge)
	maxAgeGrace, _ := time.ParseDuration(grpcConfig.MaxConnectionAgeGrace)
	options := []grpc.ServerOption{
		grpc.MaxRecvMsgSize(grpcConfig.MaxRecvMessageBytes),
		grpc.MaxSendMsgSize(grpcConfig.MaxSendMessageBytes),
		grpc.MaxConcurrentStreams(grpcConfig.MaxConcurrentStreams),
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{MinTime: minTime}),
		grpc.KeepaliveParams(keepalive.ServerParameters{
			Time:                  keepaliveTime,
			Timeout:               keepaliveTimeout,
			MaxConnectionIdle:     maxIdle,
			MaxConnectionAge:      maxAge,
			MaxConnectionAgeGrace: maxAgeGrace,
		}),
		grpc.ChainUnaryInterceptor(append(
			[]grpc.UnaryServerInterceptor{grpcUnaryRecoveryInterceptor(b.runtime.Logger), grpcUnaryObservabilityInterceptor(b.runtime.Logger)},
			unaryInterceptors...,
		)...),
		grpc.ChainStreamInterceptor(append(
			[]grpc.StreamServerInterceptor{grpcStreamRecoveryInterceptor(b.runtime.Logger), grpcStreamObservabilityInterceptor(b.runtime.Logger)},
			streamInterceptors...,
		)...),
	}
	if transportCredentials != nil {
		options = append(options, grpc.Creds(transportCredentials))
	}
	server := grpc.NewServer(options...)
	safeRegistrar := newSafeGRPCServiceRegistrar(server)
	for _, service := range services {
		if err := safeRegistrar.register(service); err != nil {
			server.Stop()
			return nil, err
		}
	}
	serviceNames := make([]string, 0, len(safeRegistrar.serviceNames))
	for serviceName := range safeRegistrar.serviceNames {
		serviceNames = append(serviceNames, serviceName)
	}
	sort.Strings(serviceNames)

	runtimeServer := &grpcRuntimeServer{Server: server, serviceNames: serviceNames}
	if grpcConfig.HealthEnabled {
		healthServer := health.NewServer()
		healthServer.SetServingStatus("", healthpb.HealthCheckResponse_NOT_SERVING)
		for _, serviceName := range serviceNames {
			healthServer.SetServingStatus(serviceName, healthpb.HealthCheckResponse_NOT_SERVING)
		}
		healthpb.RegisterHealthServer(server, healthServer)
		runtimeServer.health = healthServer
	}
	if grpcConfig.ReflectionEnabled {
		reflection.Register(server)
	}
	return runtimeServer, nil
}

func (s *grpcRuntimeServer) setServing() {
	if s != nil && s.health != nil {
		s.health.Resume()
	}
}

func (s *grpcRuntimeServer) setNotServing() {
	if s != nil && s.health != nil {
		s.health.Shutdown()
	}
}

func grpcUnaryRecoveryInterceptor(logger *slog.Logger) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, request any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (response any, err error) {
		ctx = grpcRequestContext(ctx)
		defer func() {
			if recovered := recover(); recovered != nil {
				logGRPCPanic(logger, ctx, info.FullMethod, recovered)
				response = nil
				err = status.Error(codes.Internal, "internal server error")
			}
		}()
		return handler(ctx, request)
	}
}

func grpcStreamRecoveryInterceptor(logger *slog.Logger) grpc.StreamServerInterceptor {
	return func(service any, stream grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) (err error) {
		stream = &grpcContextServerStream{ServerStream: stream, ctx: grpcRequestContext(stream.Context())}
		defer func() {
			if recovered := recover(); recovered != nil {
				logGRPCPanic(logger, stream.Context(), info.FullMethod, recovered)
				err = status.Error(codes.Internal, "internal server error")
			}
		}()
		return handler(service, stream)
	}
}

func grpcUnaryObservabilityInterceptor(logger *slog.Logger) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, request any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (response any, err error) {
		ctx = grpcRequestContext(ctx)
		start := time.Now()
		defer func() {
			if recovered := recover(); recovered != nil {
				logGRPCRequest(logger, ctx, info.FullMethod, codes.Internal, time.Since(start))
				panic(recovered)
			}
			logGRPCRequest(logger, ctx, info.FullMethod, grpcHandlerStatusCode(err), time.Since(start))
		}()
		return handler(ctx, request)
	}
}

func grpcStreamObservabilityInterceptor(logger *slog.Logger) grpc.StreamServerInterceptor {
	return func(service any, stream grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) (err error) {
		ctx := grpcRequestContext(stream.Context())
		start := time.Now()
		defer func() {
			if recovered := recover(); recovered != nil {
				logGRPCRequest(logger, ctx, info.FullMethod, codes.Internal, time.Since(start))
				panic(recovered)
			}
			logGRPCRequest(logger, ctx, info.FullMethod, grpcHandlerStatusCode(err), time.Since(start))
		}()
		return handler(service, &grpcContextServerStream{ServerStream: stream, ctx: ctx})
	}
}

type grpcContextServerStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s *grpcContextServerStream) Context() context.Context { return s.ctx }

func grpcRequestContext(ctx context.Context) context.Context {
	if existing, ok := ctx.Value(RequestIDKey).(string); ok && validRequestID.MatchString(existing) {
		return ctx
	}
	requestID := ""
	if incoming, ok := metadata.FromIncomingContext(ctx); ok {
		for _, candidate := range incoming.Get("x-request-id") {
			if validRequestID.MatchString(candidate) {
				requestID = candidate
				break
			}
		}
	}
	if requestID == "" {
		requestID = uuid.NewString()
	}
	_ = grpc.SetHeader(ctx, metadata.Pairs("x-request-id", requestID))
	return context.WithValue(ctx, RequestIDKey, requestID)
}

func grpcHandlerStatusCode(err error) codes.Code {
	if grpcStatus, ok := status.FromError(err); ok {
		return grpcStatus.Code()
	}
	return status.FromContextError(err).Code()
}

func logGRPCRequest(logger *slog.Logger, ctx context.Context, method string, code codes.Code, duration time.Duration) {
	if logger == nil {
		return
	}
	logger.InfoContext(ctx, "gRPC request handled", "method", method, "status", code.String(), "duration", duration.String())
}

func logGRPCPanic(logger *slog.Logger, ctx context.Context, method string, recovered any) {
	if logger == nil {
		return
	}
	logger.ErrorContext(ctx, "gRPC panic recovered",
		"error_code", "BEAR_GRPC_PANIC",
		"method", method,
		"category", runtimePanicCategory(recovered),
		"stack", string(debug.Stack()),
	)
}
