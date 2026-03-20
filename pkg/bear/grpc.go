package bear

import (
	"google.golang.org/grpc"
)

// GRPCService 规范化 gRPC 服务接口
type GRPCService interface {
	Bean
	Register(srv *grpc.Server)
}

// BaseGRPCService 提供基础实现
type BaseGRPCService struct {
}

func (this *BaseGRPCService) Name() string {
	return "BaseGRPCService"
}
