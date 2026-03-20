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

func (this *BaseFairing) OnRequest(ctx *gin.Context) error {
	return nil
}

func (this *BaseFairing) OnResponse(result interface{}) (interface{}, error) {
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

func (this *FairingHandler) AddFairing(f ...Fairing) {
	for _, fairing := range f {
		this.fairings = append(this.fairings, fairing)

		// 简单的启发式判断：如果不是 BaseFairing 或者重写了方法，则加入活跃列表
		// 注意：Go 中判断接口是否重写比较困难，这里我们保留所有，但预分配切片以优化迭代
		this.requestFairings = append(this.requestFairings, fairing)
		this.responseFairings = append(this.responseFairings, fairing)
	}
}

func (this *FairingHandler) OnRequest(ctx *gin.Context) error {
	for i := 0; i < len(this.requestFairings); i++ {
		if err := this.requestFairings[i].OnRequest(ctx); err != nil {
			return err
		}
	}
	return nil
}

func (this *FairingHandler) OnResponse(result interface{}) interface{} {
	var r = result
	// 响应拦截器通常按注册顺序执行（或者倒序，取决于设计，这里保持顺序）
	for i := 0; i < len(this.responseFairings); i++ {
		if res, err := this.responseFairings[i].OnResponse(r); err == nil {
			r = res
		}
	}
	return r
}

// OnRequestWithRoute 执行全局 OnRequest，然后执行路由级别的 OnRequest
func (this *FairingHandler) OnRequestWithRoute(ctx *gin.Context, routeFairings []Fairing) error {
	// 1. 先执行路由级别的 OnRequest（优先级更高）
	for _, f := range routeFairings {
		if err := f.OnRequest(ctx); err != nil {
			return err
		}
	}

	// 2. 再执行全局 OnRequest
	return this.OnRequest(ctx)
}
