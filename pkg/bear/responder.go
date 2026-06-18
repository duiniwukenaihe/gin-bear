package bear

import (
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync"

	"github.com/gin-gonic/gin"
	"github.com/nicksnyder/go-i18n/v2/i18n"
	"reflect"
)

// RouteMetadata 路由元数据，用于 Handler 预热
// handlerCache 用于缓存 handler 转换结果，避免每次请求都反射
var handlerCache = sync.Map{} // map[uintptr]gin.HandlerFunc

// Responder 定义了响应转换接口
type Responder interface {
	RespondTo() gin.HandlerFunc
}

// JSONResponse 处理 struct/map 返回，自动包装为标准格式 (兼容旧版，但建议直接使用通用签名)
type JSONResponse func(*gin.Context) Response

func (this JSONResponse) RespondTo() gin.HandlerFunc {
	return Convert((func(*gin.Context) (interface{}, error))(func(ctx *gin.Context) (interface{}, error) {
		return this(ctx), nil
	}))
}

// WarmupHandlers 在启动阶段预热所有路由的 Handler
// 将懒加载改为预热，避免首次请求时的反射开销
func WarmupHandlers(routes []RouteMetadata) {
	for _, route := range routes {
		if route.HandlerType != nil {
			// 预先转换并缓存
			Convert(route.HandlerType)
		}
	}
}

// Convert 将业务 handler 转换为 Gin 的 HandlerFunc
// 支持以下签名：
// 1. 标准 Gin Handler: func(*gin.Context)
// 2. 无 Context Handler: func(*Req) (*Res, error)
// 3. 全能 Handler: func(*gin.Context, *Req) (*Res, error)
// 4. 只有返回值的 Handler: func() (*Res, error)
func Convert(handler interface{}) gin.HandlerFunc {
	// 1. 快速检查标准 Gin Handler
	if h, ok := handler.(gin.HandlerFunc); ok {
		return h
	}
	if h, ok := handler.(func(*gin.Context)); ok {
		return h
	}

	// 2. 尝试从缓存获取
	h_ref := reflect.ValueOf(handler)
	if h_ref.IsValid() {
		if cached, ok := handlerCache.Load(h_ref.Pointer()); ok {
			return cached.(gin.HandlerFunc)
		}
	}

	// 3. 反射转换 (增强型适配器)
	h_type := h_ref.Type()

	if h_type.Kind() != reflect.Func {
		return nil
	}

	result := func(ctx *gin.Context) {
		args := make([]reflect.Value, h_type.NumIn())
		for i := 0; i < h_type.NumIn(); i++ {
			argType := h_type.In(i)

			// 情况 A: gin.Context
			if argType == reflect.TypeOf((*gin.Context)(nil)) {
				args[i] = reflect.ValueOf(ctx)
				continue
			}

			// 情况 B: 结构体指针 (自动从 JSON 绑定与验证或 Query 绑定)
			if argType.Kind() == reflect.Ptr && argType.Elem().Kind() == reflect.Struct {
				req := reflect.New(argType.Elem()).Interface()
				// 检查结构体是否有 form 标签，如果有则使用 ShouldBindQuery
				hasFormTag := false
				structType := argType.Elem()
				for j := 0; j < structType.NumField(); j++ {
					field := structType.Field(j)
					if field.Tag.Get("form") != "" || field.Tag.Get("query") != "" {
						hasFormTag = true
						break
					}
				}
				// Gin 默认会调用 validator/v10
				var err error
				if hasFormTag {
					err = ctx.ShouldBindQuery(req)
				} else {
					err = ctx.ShouldBindJSON(req)
				}
				if err != nil {
					// 记录请求参数绑定失败日志
					slog.ErrorContext(ctx.Request.Context(), "Request binding failed",
						"error", err,
						"path", ctx.Request.URL.Path,
						"method", ctx.Request.Method,
					)
					ctx.AbortWithStatusJSON(400, Response{
						Code:    400,
						Message: "Invalid request",
					})
					return
				}
				args[i] = reflect.ValueOf(req)
				continue
			}

			// 情况 C: 结构体 (非指针)
			if argType.Kind() == reflect.Struct {
				req := reflect.New(argType).Interface()
				// 检查结构体是否有 form 标签
				hasFormTag := false
				for j := 0; j < argType.NumField(); j++ {
					field := argType.Field(j)
					if field.Tag.Get("form") != "" || field.Tag.Get("query") != "" {
						hasFormTag = true
						break
					}
				}
				var err error
				if hasFormTag {
					err = ctx.ShouldBindQuery(req)
				} else {
					err = ctx.ShouldBindJSON(req)
				}
				if err != nil {
					// 记录请求参数绑定失败日志
					slog.ErrorContext(ctx.Request.Context(), "Request binding failed",
						"error", err,
						"path", ctx.Request.URL.Path,
						"method", ctx.Request.Method,
					)
					ctx.AbortWithStatusJSON(400, Response{
						Code:    400,
						Message: "Invalid request",
					})
					return
				}
				args[i] = reflect.ValueOf(req).Elem()
				continue
			}

			// 情况 D: 路径参数 (string 从 URL 提取)
			// 注意: 由于 Go 反射无法获取参数名，我们遍历所有路径参数进行匹配
			if argType.Kind() == reflect.String {
				params := ctx.Params
				for _, p := range params {
					if p.Value != "" {
						args[i] = reflect.ValueOf(p.Value)
						break
					}
				}
				continue
			}
			// 处理数值类型路径参数
			if argType.Kind() == reflect.Int || argType.Kind() == reflect.Int8 ||
				argType.Kind() == reflect.Int16 || argType.Kind() == reflect.Int32 || argType.Kind() == reflect.Int64 {
				params := ctx.Params
				for _, p := range params {
					if p.Value != "" {
						intVal, err := strconv.ParseInt(p.Value, 10, argType.Bits())
						if err != nil {
							ctx.AbortWithStatusJSON(400, Response{
								Code:    400,
								Message: "Invalid path parameter",
							})
							return
						}
						args[i] = reflect.ValueOf(intVal).Convert(argType)
						break
					}
				}
				continue
			}

			// 其他情况填充零值
			args[i] = reflect.Zero(argType)
		}

		// 执行 Handler
		results := h_ref.Call(args)

		// 处理返回值
		handleResults(ctx, results)
	}

	// 存入缓存
	if h_ref.IsValid() {
		handlerCache.Store(h_ref.Pointer(), result)
	}

	return result
}

func handleResults(ctx *gin.Context, results []reflect.Value) {
	numOut := len(results)
	if numOut == 0 {
		return
	}

	// 如果有 error 返回值（假设在最后一个）
	lastIdx := numOut - 1
	if results[lastIdx].Type().Implements(reflect.TypeOf((*error)(nil)).Elem()) {
		if !results[lastIdx].IsNil() {
			err := results[lastIdx].Interface().(error)
			handleError(ctx, err)
			return
		}
		// 如果 error 为 nil，则处理前面的返回值
		if numOut > 1 {
			result := results[0].Interface()
			// 将结果存入上下文，供路由级别 Fairing 使用
			ctx.Set("bear_handler_result", result)
			handleSuccess(ctx, result)
		} else {
			ctx.JSON(200, Response{Code: 200, Message: "success"})
		}
		return
	}

	// 没有 error 返回值，直接取第一个结果
	result := results[0].Interface()
	// 将结果存入上下文，供路由级别 Fairing 使用
	ctx.Set("bear_handler_result", result)
	handleSuccess(ctx, result)
}

func handleSuccess(ctx *gin.Context, result interface{}) {
	bear := GetByType[*Bear]()

	// 1. 首先检查是否有路由级别的 Fairing 处理过结果
	if routeResult, ok := ctx.Get("bear_route_fairing_result"); ok {
		// 如果已经有路由级别的 Fairing 处理过，使用处理后的结果
		result = routeResult
	}

	// 2. 执行全局 Fairing 后置处理
	finalResult := bear.fairingHandler.OnResponse(result)

	// 处理 I18n (如果结果是 Response 结构体)
	if res, ok := finalResult.(Response); ok {
		localizer := GetLocalizer(ctx)
		if localizer != nil && res.Message != "" {
			translated, err := localizer.Localize(&i18n.LocalizeConfig{
				MessageID: res.Message,
			})
			if err == nil {
				res.Message = translated
			}
		}

		// 敏感数据脱敏 (已禁用 - 精简模式)

		ctx.JSON(200, res)
		return
	}

	ctx.JSON(200, finalResult)
}

func handleError(ctx *gin.Context, err error) {
	msg := "Internal server error"
	status := 500
	code := 500

	// 记录 handler 执行失败日志
	slog.ErrorContext(ctx.Request.Context(), "Handler execution failed",
		"error", err,
		"path", ctx.Request.URL.Path,
		"method", ctx.Request.Method,
	)

	var be *BearError
	if errors.As(err, &be) {
		code = int(be.Code)
		status = be.Status
		if status == 0 {
			status = code
		}
		msg = be.Message
		if be.Key != "" {
			msg = be.Key
			localizer := GetLocalizer(ctx)
			if localizer != nil {
				translated, lErr := localizer.Localize(&i18n.LocalizeConfig{
					MessageID: be.Key,
				})
				if lErr == nil {
					msg = translated
				}
			}
		}
	} else if rid, ok := ctx.Get(RequestIDKey); ok {
		msg = fmt.Sprintf("Internal server error (RID: %v)", rid)
	}

	ctx.AbortWithStatusJSON(status, Response{Code: code, Message: msg})
}
