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

const strictFairingStateKey = "bear.strict_fairing_state"

type strictFairingState struct {
	globalStarted bool
	entered       []Fairing
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
	return runRequestFairings(ctx, f.requestFairings)
}

// OnResponse runs response Fairings while ignoring individual transformation errors.
//
// Deprecated: use OnResponseE when callers need conversion errors.
func (f *FairingHandler) OnResponse(result interface{}) interface{} {
	response := result
	for i := 0; i < len(f.responseFairings); i++ {
		if transformed, err := f.responseFairings[i].OnResponse(response); err == nil {
			response = transformed
		}
	}
	return response
}

// OnResponseE runs response Fairings and returns the first transformation error.
func (f *FairingHandler) OnResponseE(result any) (any, error) {
	response := result
	for _, fairing := range f.responseFairings {
		transformed, err := fairing.OnResponse(response)
		if err != nil {
			return nil, err
		}
		response = transformed
	}
	return response, nil
}

func (f *FairingHandler) onResponse(result interface{}) (interface{}, error) {
	return f.OnResponseE(result)
}

// OnRequestWithRoute 执行全局 OnRequest，然后执行路由级别的 OnRequest
func (f *FairingHandler) OnRequestWithRoute(ctx *gin.Context, routeFairings []Fairing) error {
	if err := runRequestFairings(ctx, routeFairings); err != nil {
		return err
	}
	if requestFairingTerminal(ctx) {
		return nil
	}
	return f.OnRequest(ctx)
}

func runRequestFairings(ctx *gin.Context, fairings []Fairing) error {
	for _, fairing := range fairings {
		if requestFairingTerminal(ctx) {
			return nil
		}
		if err := fairing.OnRequest(ctx); err != nil {
			return err
		}
		if requestFairingTerminal(ctx) {
			return nil
		}
	}
	return nil
}

func requestFairingTerminal(ctx *gin.Context) bool {
	return ctx == nil || ctx.IsAborted() || ctx.Writer.Written()
}

func strictFairingStateFor(ctx *gin.Context) *strictFairingState {
	if ctx == nil {
		return nil
	}
	if state, ok := ctx.Get(strictFairingStateKey); ok {
		if state, ok := state.(*strictFairingState); ok && state != nil {
			return state
		}
	}
	state := &strictFairingState{}
	ctx.Set(strictFairingStateKey, state)
	return state
}

func runEnteredRequestFairings(ctx *gin.Context, state *strictFairingState, fairings []Fairing) error {
	if state == nil || requestFairingTerminal(ctx) {
		return nil
	}
	for _, fairing := range fairings {
		if requestFairingTerminal(ctx) {
			return nil
		}
		if err := fairing.OnRequest(ctx); err != nil {
			return err
		}
		state.entered = append(state.entered, fairing)
		if requestFairingTerminal(ctx) {
			return nil
		}
	}
	return nil
}

func runEnteredResponseFairings(state *strictFairingState, result any) (any, error) {
	if state == nil {
		return result, nil
	}
	response := result
	for i := len(state.entered) - 1; i >= 0; i-- {
		transformed, err := state.entered[i].OnResponse(response)
		if err != nil {
			return nil, err
		}
		response = transformed
	}
	return response, nil
}
