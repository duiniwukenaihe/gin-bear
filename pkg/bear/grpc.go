package bear

import (
	"google.golang.org/grpc"
)

// Deprecated: GRPCService is compatibility-only. Prefer the supported HTTP lifecycle.
type GRPCService interface {
	Bean
	Register(srv *grpc.Server)
}

// Deprecated: BaseGRPCService is compatibility-only. Prefer the supported HTTP lifecycle.
type BaseGRPCService struct {
}

func (g *BaseGRPCService) Name() string {
	return "BaseGRPCService"
}
