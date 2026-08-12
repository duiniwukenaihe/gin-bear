package bear

import (
	"context"
	"errors"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

const (
	grpcTestEchoServiceName    = "bear.test.v1.EchoService"
	grpcTestEchoFullMethodName = "/" + grpcTestEchoServiceName + "/Echo"
)

type grpcTestDependency struct {
	prefix string
}

func (*grpcTestDependency) Name() string { return "grpc-test-dependency" }

type grpcTestEchoService interface {
	Echo(context.Context, *wrapperspb.StringValue) (*wrapperspb.StringValue, error)
}

type grpcTestEchoRegistrar struct {
	Dependency *grpcTestDependency `inject:"-"`
	name       string
}

func (s *grpcTestEchoRegistrar) Name() string {
	if s.name != "" {
		return s.name
	}
	return "grpc-test-echo"
}

func (s *grpcTestEchoRegistrar) RegisterGRPC(registrar grpc.ServiceRegistrar) error {
	registrar.RegisterService(&grpcTestEchoServiceDesc, s)
	return nil
}

func (s *grpcTestEchoRegistrar) Echo(_ context.Context, request *wrapperspb.StringValue) (*wrapperspb.StringValue, error) {
	prefix := ""
	if s.Dependency != nil {
		prefix = s.Dependency.prefix
	}
	return wrapperspb.String(prefix + request.GetValue()), nil
}

var grpcTestEchoServiceDesc = grpc.ServiceDesc{
	ServiceName: grpcTestEchoServiceName,
	HandlerType: (*grpcTestEchoService)(nil),
	Methods: []grpc.MethodDesc{
		{
			MethodName: "Echo",
			Handler:    grpcTestEchoHandler,
		},
	},
}

func grpcTestEchoHandler(
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
		return service.(grpcTestEchoService).Echo(ctx, request)
	}
	info := &grpc.UnaryServerInfo{
		Server:     service,
		FullMethod: grpcTestEchoFullMethodName,
	}
	handler := func(ctx context.Context, request any) (any, error) {
		return service.(grpcTestEchoService).Echo(ctx, request.(*wrapperspb.StringValue))
	}
	return interceptor(ctx, request, info, handler)
}

type grpcTestRegistrarFunc struct {
	name       string
	registerFn func(grpc.ServiceRegistrar) error
}

func (r *grpcTestRegistrarFunc) Name() string { return r.name }

func (r *grpcTestRegistrarFunc) RegisterGRPC(registrar grpc.ServiceRegistrar) error {
	if r.registerFn == nil {
		return nil
	}
	return r.registerFn(registrar)
}

type grpcTestRecordingServiceRegistrar struct {
	descriptions []*grpc.ServiceDesc
	services     []any
}

func (r *grpcTestRecordingServiceRegistrar) RegisterService(desc *grpc.ServiceDesc, service any) {
	r.descriptions = append(r.descriptions, desc)
	r.services = append(r.services, service)
}

func newGRPCTestStrictApp(t *testing.T) *Bear {
	t.Helper()
	config := NewSysConfig()
	config.SetFrameworkStrict(true)
	app, err := IgniteE(config)
	if err != nil {
		t.Fatalf("IgniteE() error = %v", err)
	}
	return app
}

func TestAddGRPCServiceERegistersBeanAndStrictlyInjectsDependency(t *testing.T) {
	app := newGRPCTestStrictApp(t)
	dependency := &grpcTestDependency{prefix: "injected:"}
	service := &grpcTestEchoRegistrar{}
	if err := app.BeansE(dependency); err != nil {
		t.Fatalf("BeansE(dependency) error = %v", err)
	}

	if err := app.AddGRPCServiceE(service); err != nil {
		t.Fatalf("AddGRPCServiceE() error = %v", err)
	}
	registered, err := ResolveE[*grpcTestEchoRegistrar](app.Runtime().Container)
	if err != nil {
		t.Fatalf("ResolveE[*grpcTestEchoRegistrar]() error = %v", err)
	}
	if registered != service {
		t.Fatalf("registered service = %p, want %p", registered, service)
	}
	if service.Dependency != nil {
		t.Fatal("gRPC service dependency was injected before ApplyAll")
	}

	if err := app.ApplyAll(context.Background()); err != nil {
		t.Fatalf("ApplyAll() error = %v", err)
	}
	if service.Dependency != dependency {
		t.Fatalf("injected dependency = %p, want %p", service.Dependency, dependency)
	}
}

func TestAddGRPCServiceEStrictInjectionRejectsMissingDependency(t *testing.T) {
	app := newGRPCTestStrictApp(t)
	service := &grpcTestEchoRegistrar{}
	if err := app.AddGRPCServiceE(service); err != nil {
		t.Fatalf("AddGRPCServiceE() error = %v", err)
	}

	if err := app.ApplyAll(context.Background()); !errors.Is(err, ErrBeanMissing) {
		t.Fatalf("ApplyAll() error = %v, want ErrBeanMissing", err)
	}
}

func TestAddGRPCServiceERejectsInvalidBatchAtomically(t *testing.T) {
	t.Run("typed nil", func(t *testing.T) {
		app := newGRPCTestStrictApp(t)
		var service *grpcTestEchoRegistrar

		err := grpcTestErrorWithoutPanic(t, func() error {
			return app.AddGRPCServiceE(service)
		})
		if err == nil {
			t.Fatal("AddGRPCServiceE(typed nil) error = nil")
		}
		if _, resolveErr := ResolveE[*grpcTestEchoRegistrar](app.Runtime().Container); !errors.Is(resolveErr, ErrBeanMissing) {
			t.Fatalf("typed nil changed container: ResolveE error = %v, want ErrBeanMissing", resolveErr)
		}
	})

	t.Run("nil", func(t *testing.T) {
		app := newGRPCTestStrictApp(t)

		err := grpcTestErrorWithoutPanic(t, func() error {
			return app.AddGRPCServiceE(nil)
		})
		if err == nil {
			t.Fatal("AddGRPCServiceE(nil) error = nil")
		}
	})

	t.Run("duplicate instance", func(t *testing.T) {
		app := newGRPCTestStrictApp(t)
		service := &grpcTestEchoRegistrar{}

		err := app.AddGRPCServiceE(service, service)
		if err == nil {
			t.Fatal("AddGRPCServiceE(duplicate instance) error = nil")
		}
		if _, resolveErr := ResolveE[*grpcTestEchoRegistrar](app.Runtime().Container); !errors.Is(resolveErr, ErrBeanMissing) {
			t.Fatalf("duplicate batch changed container: ResolveE error = %v, want ErrBeanMissing", resolveErr)
		}
	})

	t.Run("previously registered instance", func(t *testing.T) {
		app := newGRPCTestStrictApp(t)
		service := &grpcTestEchoRegistrar{}
		if err := app.AddGRPCServiceE(service); err != nil {
			t.Fatalf("first AddGRPCServiceE() error = %v", err)
		}

		if err := app.AddGRPCServiceE(service); err == nil {
			t.Fatal("second AddGRPCServiceE(same instance) error = nil")
		}
	})

	t.Run("duplicate concrete type", func(t *testing.T) {
		app := newGRPCTestStrictApp(t)
		first := &grpcTestEchoRegistrar{name: "grpc-test-first"}
		second := &grpcTestEchoRegistrar{name: "grpc-test-second"}

		err := app.AddGRPCServiceE(first, second)
		if !errors.Is(err, ErrBeanDuplicate) {
			t.Fatalf("AddGRPCServiceE(duplicate type) error = %v, want ErrBeanDuplicate", err)
		}
		if _, resolveErr := ResolveE[*grpcTestEchoRegistrar](app.Runtime().Container); !errors.Is(resolveErr, ErrBeanMissing) {
			t.Fatalf("invalid batch changed container: ResolveE error = %v, want ErrBeanMissing", resolveErr)
		}
	})
}

func TestAddGRPCServiceERegistrationFreezesAfterApplyAll(t *testing.T) {
	app := newGRPCTestStrictApp(t)
	if err := app.ApplyAll(context.Background()); err != nil {
		t.Fatalf("ApplyAll() error = %v", err)
	}

	service := &grpcTestEchoRegistrar{}
	if err := app.AddGRPCServiceE(service); !errors.Is(err, ErrLifecycleRegistrationClosed) {
		t.Fatalf("AddGRPCServiceE() after ApplyAll error = %v, want ErrLifecycleRegistrationClosed", err)
	}
	if _, err := ResolveE[*grpcTestEchoRegistrar](app.Runtime().Container); !errors.Is(err, ErrBeanMissing) {
		t.Fatalf("closed registration changed container: ResolveE error = %v, want ErrBeanMissing", err)
	}

	unary := func(ctx context.Context, request any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		return handler(ctx, request)
	}
	if err := app.AddGRPCUnaryInterceptorE(unary); !errors.Is(err, ErrLifecycleRegistrationClosed) {
		t.Fatalf("AddGRPCUnaryInterceptorE() after ApplyAll error = %v, want ErrLifecycleRegistrationClosed", err)
	}
	stream := func(service any, stream grpc.ServerStream, _ *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		return handler(service, stream)
	}
	if err := app.AddGRPCStreamInterceptorE(stream); !errors.Is(err, ErrLifecycleRegistrationClosed) {
		t.Fatalf("AddGRPCStreamInterceptorE() after ApplyAll error = %v, want ErrLifecycleRegistrationClosed", err)
	}
}

func TestSafeGRPCServiceRegistrarRegistersManualEchoService(t *testing.T) {
	server := grpc.NewServer()
	safe := newSafeGRPCServiceRegistrar(server)

	if err := safe.register(&grpcTestEchoRegistrar{}); err != nil {
		t.Fatalf("register(valid echo) error = %v", err)
	}
	if _, ok := server.GetServiceInfo()[grpcTestEchoServiceName]; !ok {
		t.Fatalf("registered services = %v, want %q", server.GetServiceInfo(), grpcTestEchoServiceName)
	}
}

func TestSafeGRPCServiceRegistrarRejectsUnsafeRegistration(t *testing.T) {
	registrationErr := errors.New("grpc test registration error")
	tests := []struct {
		name       string
		registrar  GRPCServiceRegistrar
		wantText   string
		wantTarget error
	}{
		{
			name: "empty service name",
			registrar: grpcTestServiceDescRegistrar("empty-service-name", grpc.ServiceDesc{
				HandlerType: (*grpcTestEchoService)(nil),
			}, &grpcTestEchoRegistrar{}),
			wantText: "service name",
		},
		{
			name: "reserved health service name",
			registrar: grpcTestServiceDescRegistrar("reserved-health", grpc.ServiceDesc{
				ServiceName: "grpc.health.v1.Health",
				HandlerType: (*grpcTestEchoService)(nil),
			}, &grpcTestEchoRegistrar{}),
			wantText: "reserved",
		},
		{
			name: "reserved reflection v1 service name",
			registrar: grpcTestServiceDescRegistrar("reserved-reflection-v1", grpc.ServiceDesc{
				ServiceName: "grpc.reflection.v1.ServerReflection",
				HandlerType: (*grpcTestEchoService)(nil),
			}, &grpcTestEchoRegistrar{}),
			wantText: "reserved",
		},
		{
			name: "reserved reflection v1alpha service name",
			registrar: grpcTestServiceDescRegistrar("reserved-reflection-v1alpha", grpc.ServiceDesc{
				ServiceName: "grpc.reflection.v1alpha.ServerReflection",
				HandlerType: (*grpcTestEchoService)(nil),
			}, &grpcTestEchoRegistrar{}),
			wantText: "reserved",
		},
		{
			name: "duplicate service name",
			registrar: &grpcTestRegistrarFunc{
				name: "duplicate-service-name",
				registerFn: func(registrar grpc.ServiceRegistrar) error {
					first := grpcTestEchoServiceDesc
					second := grpcTestEchoServiceDesc
					registrar.RegisterService(&first, &grpcTestEchoRegistrar{})
					registrar.RegisterService(&second, &grpcTestEchoRegistrar{})
					return nil
				},
			},
			wantText: "duplicate",
		},
		{
			name: "incompatible implementation",
			registrar: grpcTestServiceDescRegistrar(
				"incompatible-implementation",
				grpcTestEchoServiceDesc,
				struct{}{},
			),
			wantText: "implement",
		},
		{
			name: "nil implementation",
			registrar: grpcTestServiceDescRegistrar(
				"nil-implementation",
				grpcTestEchoServiceDesc,
				nil,
			),
			wantText: "implementation",
		},
		{
			name: "empty method name",
			registrar: grpcTestServiceDescRegistrar("empty-method-name", grpc.ServiceDesc{
				ServiceName: "bear.test.v1.EmptyMethodService",
				HandlerType: (*grpcTestEchoService)(nil),
				Methods: []grpc.MethodDesc{{
					Handler: grpcTestEchoHandler,
				}},
			}, &grpcTestEchoRegistrar{}),
			wantText: "method name",
		},
		{
			name: "nil method handler",
			registrar: grpcTestServiceDescRegistrar("nil-method-handler", grpc.ServiceDesc{
				ServiceName: "bear.test.v1.NilMethodHandlerService",
				HandlerType: (*grpcTestEchoService)(nil),
				Methods: []grpc.MethodDesc{{
					MethodName: "Echo",
				}},
			}, &grpcTestEchoRegistrar{}),
			wantText: "method handler",
		},
		{
			name: "empty stream name",
			registrar: grpcTestServiceDescRegistrar("empty-stream-name", grpc.ServiceDesc{
				ServiceName: "bear.test.v1.EmptyStreamService",
				HandlerType: (*grpcTestEchoService)(nil),
				Streams:     []grpc.StreamDesc{{Handler: func(any, grpc.ServerStream) error { return nil }}},
			}, &grpcTestEchoRegistrar{}),
			wantText: "stream name",
		},
		{
			name: "nil stream handler",
			registrar: grpcTestServiceDescRegistrar("nil-stream-handler", grpc.ServiceDesc{
				ServiceName: "bear.test.v1.NilStreamHandlerService",
				HandlerType: (*grpcTestEchoService)(nil),
				Streams:     []grpc.StreamDesc{{StreamName: "Echo"}},
			}, &grpcTestEchoRegistrar{}),
			wantText: "stream handler",
		},
		{
			name: "registration panic",
			registrar: &grpcTestRegistrarFunc{
				name: "registration-panic",
				registerFn: func(grpc.ServiceRegistrar) error {
					panic("grpc test registration panic")
				},
			},
			wantText: "panic",
		},
		{
			name: "registration returned error",
			registrar: &grpcTestRegistrarFunc{
				name:       "registration-error",
				registerFn: func(grpc.ServiceRegistrar) error { return registrationErr },
			},
			wantTarget: registrationErr,
		},
		{
			name: "zero services",
			registrar: &grpcTestRegistrarFunc{
				name:       "zero-services",
				registerFn: func(grpc.ServiceRegistrar) error { return nil },
			},
			wantText: "no service",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			target := &grpcTestRecordingServiceRegistrar{}
			safe := newSafeGRPCServiceRegistrar(target)
			err := grpcTestErrorWithoutPanic(t, func() error {
				return safe.register(test.registrar)
			})
			if err == nil {
				t.Fatal("register() error = nil")
			}
			if test.wantTarget != nil && !errors.Is(err, test.wantTarget) {
				t.Fatalf("register() error = %v, want wrapped %v", err, test.wantTarget)
			}
			if test.wantText != "" && !strings.Contains(strings.ToLower(err.Error()), test.wantText) {
				t.Fatalf("register() error = %q, want text %q", err, test.wantText)
			}
		})
	}
}

func grpcTestServiceDescRegistrar(name string, desc grpc.ServiceDesc, implementation any) GRPCServiceRegistrar {
	return &grpcTestRegistrarFunc{
		name: name,
		registerFn: func(registrar grpc.ServiceRegistrar) error {
			registrar.RegisterService(&desc, implementation)
			return nil
		},
	}
}

func grpcTestErrorWithoutPanic(t *testing.T, operation func() error) (err error) {
	t.Helper()
	defer func() {
		if recovered := recover(); recovered != nil {
			t.Fatalf("operation panicked instead of returning an error: %v", recovered)
		}
	}()
	return operation()
}
