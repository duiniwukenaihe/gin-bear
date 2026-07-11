package bear

import (
	"github.com/gin-gonic/gin"
)

// Fairing 是中间件拦截器接口
type Fairing interface {
	OnRequest(*gin.Context) error
	OnResponse(interface{}) (interface{}, error)
}

// BaseFairing 提供默认实现
type BaseFairing struct{}

func (f *BaseFairing) OnRequest(ctx *gin.Context) error {
	return nil
}

func (f *BaseFairing) OnResponse(result interface{}) (interface{}, error) {
	return result, nil
}

// IPolicy 定义动态路由策略 (阶段 66)
type IPolicy interface {
	Match(ctx *gin.Context) bool
	Fairings() []Fairing
}

// IInterceptors 允许控制器自定义拦截器
type IInterceptors interface {
	Interceptors() []Fairing
}

// FairingHandler 管理拦截器
type FairingHandler struct {
	fairings         []Fairing
	requestFairings  []Fairing
	responseFairings []Fairing
}

func NewFairingHandler() *FairingHandler {
	return &FairingHandler{
		fairings:         []Fairing{},
		requestFairings:  []Fairing{},
		responseFairings: []Fairing{},
	}
}

func (f *FairingHandler) AddFairing(fairings ...Fairing) {
	for _, fairing := range fairings {
		f.fairings = append(f.fairings, fairing)

		// 简单的启发式判断：如果不是 BaseFairing 或者重写了方法，则加入活跃列表
		// 注意：Go 中判断接口是否重写比较困难，这里我们保留所有，但预分配切片以优化迭代
		f.requestFairings = append(f.requestFairings, fairing)
		f.responseFairings = append(f.responseFairings, fairing)
	}
}

func (f *FairingHandler) OnRequest(ctx *gin.Context) error {
	for i := 0; i < len(f.requestFairings); i++ {
		if err := f.requestFairings[i].OnRequest(ctx); err != nil {
			return err
		}
	}
	return nil
}

func (f *FairingHandler) OnResponse(result interface{}) interface{} {
	response := result
	for i := 0; i < len(f.responseFairings); i++ {
		if transformed, err := f.responseFairings[i].OnResponse(response); err == nil {
			response = transformed
		}
	}
	return response
}

func (f *FairingHandler) onResponse(result interface{}) (interface{}, error) {
	response := result
	for i := 0; i < len(f.responseFairings); i++ {
		transformed, err := f.responseFairings[i].OnResponse(response)
		if err != nil {
			return nil, err
		}
		response = transformed
	}
	return response, nil
}

// OnRequestWithRoute 执行全局 OnRequest，然后执行路由级别的 OnRequest
func (f *FairingHandler) OnRequestWithRoute(ctx *gin.Context, routeFairings []Fairing) error {
	// 1. 先执行路由级别的 OnRequest（优先级更高）
	for _, f := range routeFairings {
		if err := f.OnRequest(ctx); err != nil {
			return err
		}
	}

	// 2. 再执行全局 OnRequest
	return f.OnRequest(ctx)
}
