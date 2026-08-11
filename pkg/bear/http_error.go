package bear

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/nicksnyder/go-i18n/v2/i18n"
)

// WriteError writes one safe JSON response for err. BearError controls valid
// 4xx and 5xx statuses; invalid statuses and unexpected errors become HTTP 500.
// Wrapped causes are logged with request metadata and are never sent to clients.
func WriteError(ctx *gin.Context, err error) {
	if ctx == nil {
		return
	}
	ctx.Abort()
	status, response := errorResponse(ctx, err)
	logHTTPError(ctx, err, status, response.Code)
	if ctx.Writer.Written() {
		return
	}
	ctx.AbortWithStatusJSON(status, response)
}

func logHTTPError(ctx *gin.Context, err error, status, errorCode int) {
	logger := legacyLogger()
	if value, ok := ctx.Get(runtimeContextKey); ok {
		if runtime, ok := value.(*Runtime); ok && runtime != nil && runtime.Logger != nil {
			logger = runtime.Logger
		}
	}
	requestContext := context.Background()
	method := "OTHER"
	if ctx.Request != nil {
		requestContext = ctx.Request.Context()
		method = normalizeHTTPMethod(ctx.Request.Method)
	}
	category, stableCode := observableErrorMetadata(err, status)
	if stableCode == 0 {
		stableCode = errorCode
	}
	logger.ErrorContext(requestContext, "Handler execution failed",
		"error_category", category,
		"error_code", stableCode,
		"status", status,
		"route", tracingRoute(ctx),
		"method", method,
	)
}

func errorResponse(ctx *gin.Context, err error) (int, Response) {
	status := http.StatusInternalServerError
	code := http.StatusInternalServerError
	message := "Internal server error"

	var bearError *BearError
	if !errors.As(err, &bearError) || bearError == nil {
		if requestID, ok := ctx.Get(RequestIDKey); ok {
			message = fmt.Sprintf("Internal server error (RID: %v)", requestID)
		}
		return status, Response{Code: code, Message: message}
	}

	candidate := bearError.Status
	if candidate == 0 {
		candidate = bearError.Code
	}
	if candidate < http.StatusBadRequest || candidate > 599 {
		return status, Response{Code: code, Message: message}
	}

	status = candidate
	code = bearError.Code
	if code == 0 {
		code = status
	}
	message = safeBearErrorMessage(ctx, bearError, status)
	return status, Response{Code: code, Message: message}
}

func safeBearErrorMessage(ctx *gin.Context, err *BearError, status int) string {
	if status >= http.StatusInternalServerError {
		return "Internal server error"
	}
	if err.Key != "" {
		if localizer := GetLocalizer(ctx); localizer != nil {
			message, localizeErr := localizer.Localize(&i18n.LocalizeConfig{
				MessageID:    err.Key,
				TemplateData: err.Args,
			})
			if localizeErr == nil && message != "" {
				return message
			}
		}
	}
	if err.Message != "" {
		return err.Message
	}
	if registered := registeredErrorMessage(err.Code, err.Key); registered != "" {
		return registered
	}
	if err.Key != "" && !strings.HasPrefix(err.Key, "error_") {
		return err.Key
	}
	if message := http.StatusText(status); message != "" {
		return message
	}
	return "Request failed"
}

func registeredErrorMessage(code int, key string) string {
	defaultRegistry.mu.RLock()
	defer defaultRegistry.mu.RUnlock()
	definition := defaultRegistry.errors[code]
	if definition == nil || (key != "" && definition.Key != key) {
		return ""
	}
	return definition.Message
}
