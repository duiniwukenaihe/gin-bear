package bear

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/nicksnyder/go-i18n/v2/i18n"
)

// Responder defines a response adapter that can be mounted by Gin.
type Responder interface {
	RespondTo() gin.HandlerFunc
}

// JSONResponse adapts a context-aware Response producer.
type JSONResponse func(*gin.Context) Response

// StatusResponse lets a handler set the HTTP status for a successful response.
type StatusResponse struct {
	Status int
	Value  any
}

// WithStatus returns a successful response with an explicit HTTP status.
func WithStatus(status int, value any) StatusResponse {
	return StatusResponse{Status: status, Value: value}
}

func (r JSONResponse) RespondTo() gin.HandlerFunc {
	return Convert(func(ctx *gin.Context) (interface{}, error) {
		return r(ctx), nil
	})
}

// WarmupHandlers is retained for source compatibility. Handlers are now
// compiled from their concrete function values when routes are constructed.
//
// Deprecated: route registration performs the only required compilation.
func WarmupHandlers(routes []RouteMetadata) { _ = routes }

// Convert compiles a business handler into a Gin handler during construction.
// A standard gin.HandlerFunc is returned unchanged and remains an opaque
// response writer; Bear cannot transform a response that the function writes.
// Invalid handler signatures panic. Use Bear.HandleE when construction errors
// need to be returned instead.
func Convert(handler interface{}) gin.HandlerFunc {
	if opaque, ok := opaqueGinHandler(handler); ok {
		return opaque
	}
	compiled, err := compileHandler(handler)
	if err != nil {
		panic(err)
	}
	return func(ctx *gin.Context) {
		result, err := compiled(ctx)
		if err != nil {
			WriteError(ctx, err)
			return
		}
		writeSuccess(ctx, result)
	}
}

func writeSuccess(ctx *gin.Context, result interface{}) {
	var config *SysConfig
	if ctx != nil {
		if value, ok := ctx.Get(runtimeContextKey); ok {
			if runtime, ok := value.(*Runtime); ok && runtime != nil {
				config = runtime.Config
			}
		}
	}
	writeSuccessWithConfig(ctx, config, result)
}

func writeSuccessWithConfig(ctx *gin.Context, config *SysConfig, result any) {
	if ctx == nil || ctx.Writer.Written() {
		return
	}

	status := http.StatusOK
	if response, ok := result.(StatusResponse); ok {
		status = response.Status
		result = response.Value
	}
	if status < http.StatusOK || status > 599 {
		WriteError(ctx, fmt.Errorf("invalid successful response status %d", status))
		return
	}

	if response, ok := result.(Response); ok {
		if localizer := GetLocalizer(ctx); localizer != nil && response.Message != "" {
			translated, err := localizer.Localize(&i18n.LocalizeConfig{MessageID: response.Message})
			if err == nil {
				response.Message = translated
			}
		}
		result = response
	} else if config != nil && config.ResponseMode() == "envelope" {
		result = Response{Code: status, Message: "success", Data: result}
	} else if result == nil {
		result = Response{Code: status, Message: "success"}
	}

	if status == http.StatusNoContent || status == http.StatusNotModified || ctx.Request != nil && ctx.Request.Method == http.MethodHead {
		ctx.Status(status)
		return
	}

	body, err := json.Marshal(result)
	if err != nil {
		WriteError(ctx, fmt.Errorf("marshal successful response: %w", err))
		return
	}
	ctx.Header("Content-Type", "application/json; charset=utf-8")
	ctx.Status(status)
	_, _ = ctx.Writer.Write(body)
}
