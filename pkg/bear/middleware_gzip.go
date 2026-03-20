package bear

import (
	"compress/gzip"
	"strings"

	"github.com/gin-gonic/gin"
)

// GzipFairing 提供响应压缩功能
type GzipFairing struct {
	BaseFairing
	MinLength int
}

func NewGzipFairing(minLength int) *GzipFairing {
	if minLength <= 0 {
		minLength = 1024 // 默认 1KB
	}
	return &GzipFairing{MinLength: minLength}
}

type gzipWriter struct {
	gin.ResponseWriter
	writer *gzip.Writer
}

func (g *gzipWriter) Write(data []byte) (int, error) {
	return g.writer.Write(data)
}

func (g *gzipWriter) WriteString(s string) (int, error) {
	return g.writer.Write([]byte(s))
}

func (this *GzipFairing) OnRequest(ctx *gin.Context) error {
	// 检查客户端是否支持 gzip
	if !strings.Contains(ctx.GetHeader("Accept-Encoding"), "gzip") {
		return nil
	}

	// 检查是否已经是压缩过的
	if ctx.GetHeader("Content-Encoding") == "gzip" {
		return nil
	}

	// 我们在 OnResponse 中进行实际的写入拦截，
	// 但 OnRequest 可以预设一些标志或者准备工作
	return nil
}

// 注意：由于 gin 的 ResponseWriter 写入通常在 Handler 执行完之后，
// 我们通过一个简单的 Gin 中间件来配合 GzipFairing 可能会更优雅，
// 但为了保持 Fairing 架构的纯粹性，我们这里展示如何集成。
// 实际上，Gzip 这种流式操作更适合作为 Gin Middleware。

func GzipMiddleware(minLength int) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !strings.Contains(c.GetHeader("Accept-Encoding"), "gzip") {
			c.Next()
			return
		}

		gz, _ := gzip.NewWriterLevel(c.Writer, gzip.BestSpeed)
		defer gz.Close()

		c.Header("Content-Encoding", "gzip")
		c.Header("Vary", "Accept-Encoding")

		oldWriter := c.Writer
		c.Writer = &gzipWriter{ResponseWriter: oldWriter, writer: gz}

		c.Next()
	}
}
