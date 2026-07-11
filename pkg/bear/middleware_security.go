package bear

import (
	"errors"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"
)

const defaultHTTPSizeLimit = 1 << 20

func securityHeadersMiddleware() gin.HandlerFunc {
	return func(ctx *gin.Context) {
		ctx.Header("X-Content-Type-Options", "nosniff")
		ctx.Header("X-Frame-Options", "DENY")
		ctx.Header("Referrer-Policy", "no-referrer")
		ctx.Next()
	}
}

func effectiveRequestBodyLimit(config *SysConfig) int64 {
	if config == nil || config.Server == nil || config.Server.MaxRequestBodyBytes == 0 {
		return defaultHTTPSizeLimit
	}
	return config.Server.MaxRequestBodyBytes
}

func requestBodyLimitMiddleware(limit int64) gin.HandlerFunc {
	return func(ctx *gin.Context) {
		if limit <= 0 {
			ctx.Next()
			return
		}
		if ctx.Request.ContentLength > limit {
			ctx.AbortWithStatus(http.StatusRequestEntityTooLarge)
			return
		}
		if ctx.Request.Body != nil {
			ctx.Request.Body = &limitedRequestBody{
				ReadCloser: http.MaxBytesReader(ctx.Writer, ctx.Request.Body, limit),
				ctx:        ctx,
			}
		}
		ctx.Next()
	}
}

type limitedRequestBody struct {
	io.ReadCloser
	ctx *gin.Context
}

func (body *limitedRequestBody) Read(data []byte) (int, error) {
	n, err := body.ReadCloser.Read(data)
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) && !body.ctx.Writer.Written() {
		body.ctx.AbortWithStatus(http.StatusRequestEntityTooLarge)
	}
	return n, err
}
