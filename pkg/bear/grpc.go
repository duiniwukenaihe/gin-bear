package bear

import (
	"errors"
	"fmt"
	"reflect"
	"strings"

	"google.golang.org/grpc"
)

// Deprecated: GRPCService is the legacy registration contract. New services
// should implement GRPCServiceRegistrar and use AddGRPCServiceE.
type GRPCService interface {
	Bean
	Register(srv *grpc.Server)
}

// GRPCServiceRegistrar registers one or more gRPC services during application
// startup. Implementations are application Beans and receive dependency
// injection before RegisterGRPC is called.
type GRPCServiceRegistrar interface {
	Bean
	RegisterGRPC(grpc.ServiceRegistrar) error
}

// Deprecated: BaseGRPCService only supports the legacy GRPCService contract.
type BaseGRPCService struct {
}

func (g *BaseGRPCService) Name() string {
	return "BaseGRPCService"
}

// AddGRPCServiceE registers injectable gRPC services before application
// startup. A failed batch leaves both the Container and service registry
// unchanged.
func (b *Bear) AddGRPCServiceE(services ...GRPCServiceRegistrar) error {
	if b == nil || b.runtime == nil {
		return errors.New("bear runtime is unavailable")
	}
	beans := make([]Bean, len(services))
	for index, service := range services {
		if service == nil || isNilBean(service) {
			return fmt.Errorf("gRPC service item %d (%T) must not be nil", index, service)
		}
		beans[index] = service
	}
	values, names, err := prepareStrictBeans(beans)
	if err != nil {
		return fmt.Errorf("register gRPC services: %w", err)
	}

	b.eRegistrationMu.Lock()
	defer b.eRegistrationMu.Unlock()
	for index, service := range services {
		for previous := 0; previous < index; previous++ {
			if sameBeanInstance(services[previous], service) {
				return fmt.Errorf("gRPC service item %d duplicates item %d", index, previous)
			}
		}
		for _, registered := range b.grpcServiceRegistrars {
			if sameBeanInstance(registered, service) {
				return fmt.Errorf("gRPC service %q is already registered", service.Name())
			}
		}
	}
	if err := b.runtime.Container.trySetBatchStrict(values); err != nil {
		return fmt.Errorf("register gRPC services: %w", err)
	}
	publishBeanMetadata(b.exprData, beans, names)
	b.grpcServiceRegistrars = append(b.grpcServiceRegistrars, services...)
	b.strictRegistrationVersion++
	return nil
}

// AddGRPCUnaryInterceptorE registers unary interceptors in execution order.
func (b *Bear) AddGRPCUnaryInterceptorE(interceptors ...grpc.UnaryServerInterceptor) error {
	if b == nil || b.runtime == nil {
		return errors.New("bear runtime is unavailable")
	}
	for index, interceptor := range interceptors {
		if interceptor == nil {
			return fmt.Errorf("gRPC unary interceptor item %d must not be nil", index)
		}
	}
	b.eRegistrationMu.Lock()
	defer b.eRegistrationMu.Unlock()
	return b.runtime.Lifecycle.registerBeans(nil, func() {
		b.grpcUnaryInterceptors = append(b.grpcUnaryInterceptors, interceptors...)
		b.strictRegistrationVersion++
	})
}

// AddGRPCStreamInterceptorE registers stream interceptors in execution order.
func (b *Bear) AddGRPCStreamInterceptorE(interceptors ...grpc.StreamServerInterceptor) error {
	if b == nil || b.runtime == nil {
		return errors.New("bear runtime is unavailable")
	}
	for index, interceptor := range interceptors {
		if interceptor == nil {
			return fmt.Errorf("gRPC stream interceptor item %d must not be nil", index)
		}
	}
	b.eRegistrationMu.Lock()
	defer b.eRegistrationMu.Unlock()
	return b.runtime.Lifecycle.registerBeans(nil, func() {
		b.grpcStreamInterceptors = append(b.grpcStreamInterceptors, interceptors...)
		b.strictRegistrationVersion++
	})
}

func (b *Bear) grpcRegistrationSnapshot() ([]GRPCServiceRegistrar, []grpc.UnaryServerInterceptor, []grpc.StreamServerInterceptor) {
	b.eRegistrationMu.Lock()
	defer b.eRegistrationMu.Unlock()
	return append([]GRPCServiceRegistrar(nil), b.grpcServiceRegistrars...),
		append([]grpc.UnaryServerInterceptor(nil), b.grpcUnaryInterceptors...),
		append([]grpc.StreamServerInterceptor(nil), b.grpcStreamInterceptors...)
}

var reservedGRPCServiceNames = map[string]struct{}{
	"grpc.health.v1.Health":                    {},
	"grpc.reflection.v1.ServerReflection":      {},
	"grpc.reflection.v1alpha.ServerReflection": {},
}

type safeGRPCServiceRegistrar struct {
	server       grpc.ServiceRegistrar
	serviceNames map[string]struct{}
	err          error
}

func newSafeGRPCServiceRegistrar(server grpc.ServiceRegistrar) *safeGRPCServiceRegistrar {
	return &safeGRPCServiceRegistrar{
		server:       server,
		serviceNames: make(map[string]struct{}),
	}
}

// RegisterService implements grpc.ServiceRegistrar. Validation errors are
// retained and returned by register because generated registration helpers do
// not expose an error result.
func (r *safeGRPCServiceRegistrar) RegisterService(desc *grpc.ServiceDesc, implementation any) {
	if r == nil || r.err != nil {
		return
	}
	if err := validateGRPCServiceRegistration(desc, implementation, r.serviceNames); err != nil {
		r.err = err
		return
	}
	if r.server == nil {
		r.err = errors.New("gRPC server registrar is unavailable")
		return
	}
	r.server.RegisterService(desc, implementation)
	r.serviceNames[desc.ServiceName] = struct{}{}
}

func (r *safeGRPCServiceRegistrar) register(service GRPCServiceRegistrar) (err error) {
	if r == nil {
		return errors.New("gRPC service registrar is unavailable")
	}
	if service == nil || isNilBean(service) {
		return errors.New("gRPC service registrar must not be nil")
	}
	before := len(r.serviceNames)
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("gRPC service %q registration panic: %v", service.Name(), recovered)
		}
	}()
	if err := service.RegisterGRPC(r); err != nil {
		return fmt.Errorf("register gRPC service %q: %w", service.Name(), err)
	}
	if r.err != nil {
		return fmt.Errorf("register gRPC service %q: %w", service.Name(), r.err)
	}
	if len(r.serviceNames) == before {
		return fmt.Errorf("register gRPC service %q: no service was registered", service.Name())
	}
	return nil
}

func validateGRPCServiceRegistration(desc *grpc.ServiceDesc, implementation any, names map[string]struct{}) error {
	if desc == nil {
		return errors.New("gRPC service description must not be nil")
	}
	serviceName := strings.TrimSpace(desc.ServiceName)
	if serviceName == "" {
		return errors.New("gRPC service name must not be empty")
	}
	if serviceName != desc.ServiceName {
		return errors.New("gRPC service name must not contain surrounding whitespace")
	}
	if _, reserved := reservedGRPCServiceNames[serviceName]; reserved {
		return fmt.Errorf("gRPC service name %q is reserved by the framework", serviceName)
	}
	if _, duplicate := names[serviceName]; duplicate {
		return fmt.Errorf("duplicate gRPC service name %q", serviceName)
	}
	if desc.HandlerType == nil {
		return fmt.Errorf("gRPC service %q handler type must not be nil", serviceName)
	}
	handlerType := reflect.TypeOf(desc.HandlerType)
	if handlerType.Kind() != reflect.Ptr || handlerType.Elem().Kind() != reflect.Interface {
		return fmt.Errorf("gRPC service %q handler type must point to an interface", serviceName)
	}
	if implementation == nil || isNilBean(implementation) {
		return fmt.Errorf("gRPC service %q implementation must not be nil", serviceName)
	}
	implementationType := reflect.TypeOf(implementation)
	if !implementationType.Implements(handlerType.Elem()) {
		return fmt.Errorf("gRPC service %q implementation %s does not implement %s", serviceName, implementationType, handlerType.Elem())
	}
	rpcNames := make(map[string]struct{}, len(desc.Methods)+len(desc.Streams))
	for index, method := range desc.Methods {
		if err := validateGRPCRPCDescription(serviceName, "method", index, method.MethodName, method.Handler != nil, rpcNames); err != nil {
			return err
		}
	}
	for index, stream := range desc.Streams {
		if err := validateGRPCRPCDescription(serviceName, "stream", index, stream.StreamName, stream.Handler != nil, rpcNames); err != nil {
			return err
		}
	}
	return nil
}

func validateGRPCRPCDescription(serviceName, kind string, index int, name string, hasHandler bool, names map[string]struct{}) error {
	trimmedName := strings.TrimSpace(name)
	if trimmedName == "" {
		return fmt.Errorf("gRPC service %q %s name at index %d must not be empty", serviceName, kind, index)
	}
	if trimmedName != name {
		return fmt.Errorf("gRPC service %q %s name %q must not contain surrounding whitespace", serviceName, kind, name)
	}
	if !hasHandler {
		return fmt.Errorf("gRPC service %q %s handler %q must not be nil", serviceName, kind, name)
	}
	if _, duplicate := names[name]; duplicate {
		return fmt.Errorf("gRPC service %q has duplicate RPC name %q", serviceName, name)
	}
	names[name] = struct{}{}
	return nil
}
