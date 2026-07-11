package bear

import (
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

func (r JSONResponse) RespondTo() gin.HandlerFunc {
	return Convert(func(ctx *gin.Context) (interface{}, error) {
		return r(ctx), nil
	})
}

// WarmupHandlers is retained for source compatibility. Handlers are now
// compiled from their concrete function values when routes are constructed.
//
// Deprecated: route registration performs the only required compilation.
func WarmupHandlers([]RouteMetadata) {}

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
	if ctx == nil || ctx.Writer.Written() {
		return
	}
	if response, ok := result.(Response); ok {
		if localizer := GetLocalizer(ctx); localizer != nil && response.Message != "" {
			translated, err := localizer.Localize(&i18n.LocalizeConfig{MessageID: response.Message})
			if err == nil {
				response.Message = translated
			}
		}
		ctx.JSON(http.StatusOK, response)
		return
	}
	if result == nil {
		ctx.JSON(http.StatusOK, Response{Code: http.StatusOK, Message: "success"})
		return
	}
	ctx.JSON(http.StatusOK, result)
}
