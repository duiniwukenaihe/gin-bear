package bear

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

var (
	TotalRequests int64
	TotalErrors   int64
)

// ContextKey 自定义 context key 类型，避免字符串冲突
type ContextKey string

const RequestIDKey ContextKey = "request_id"

var validRequestID = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)

// RequestIDMiddleware 为请求注入唯一标识，并传播到 context
func RequestIDMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		rid := c.GetHeader("X-Request-ID")
		if !validRequestID.MatchString(rid) {
			rid = uuid.New().String()
		}
		c.Set(RequestIDKey, rid)
		c.Header("X-Request-ID", rid)

		// 注入到 Go 原生 context 中，方便 slog 和下游链路追踪
		ctx := context.WithValue(c.Request.Context(), RequestIDKey, rid)
		c.Request = c.Request.WithContext(ctx)

		c.Next()
	}
}

// CORSMiddleware 处理跨域请求
func CORSMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		cfg := corsConfigForRequest(c)
		if cfg == nil || cfg.CORS == nil || !cfg.CORS.Enabled {
			c.Next()
			return
		}

		origin := c.GetHeader("Origin")
		if origin != "" && corsOriginAllowed(origin, cfg.CORS.AllowOrigins, cfg.CORS.AllowCredentials) {
			c.Writer.Header().Set("Access-Control-Allow-Origin", origin)
			c.Writer.Header().Set("Vary", "Origin")
		}
		if cfg.CORS.AllowCredentials {
			c.Writer.Header().Set("Access-Control-Allow-Credentials", "true")
		}
		if len(cfg.CORS.AllowHeaders) > 0 {
			c.Writer.Header().Set("Access-Control-Allow-Headers", strings.Join(cfg.CORS.AllowHeaders, ", "))
		}
		if len(cfg.CORS.AllowMethods) > 0 {
			c.Writer.Header().Set("Access-Control-Allow-Methods", strings.Join(cfg.CORS.AllowMethods, ", "))
		}
		if maxAge := parseDurationOrDefault(cfg.CORS.MaxAge, 0); maxAge > 0 {
			c.Writer.Header().Set("Access-Control-Max-Age", fmt.Sprintf("%.0f", maxAge.Seconds()))
		}

		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(204)
			return
		}

		c.Next()
	}
}

func corsConfigForRequest(c *gin.Context) *SysConfig {
	if value, exists := c.Get(runtimeContextKey); exists {
		runtime, _ := value.(*Runtime)
		if runtime == nil {
			return nil
		}
		return runtime.Config
	}
	// Compatibility for bare Gin engines that do not install Bear ownership.
	return GetByType[*SysConfig]()
}

func corsOriginAllowed(origin string, allowedOrigins []string, allowCredentials bool) bool {
	for _, allowed := range allowedOrigins {
		if allowed == origin {
			return true
		}
		if allowed == "*" && !allowCredentials {
			return true
		}
	}
	return false
}

// PerformanceMiddleware 记录请求处理耗时与基础指标
// 日志级别可通过配置文件 middleware.performance_log_level 调整 (debug, info, warn, error)
// 慢请求阈值可通过 middleware.slow_request_threshold 调整 (如 "1s", "500ms")
func PerformanceMiddleware() gin.HandlerFunc {
	// 读取配置
	cfg := GetByType[*SysConfig]()
	var logLevel slog.Level
	var slowThreshold time.Duration

	if cfg != nil && cfg.Middleware != nil {
		// 解析日志级别
		switch cfg.Middleware.PerformanceLogLevel {
		case "debug":
			logLevel = slog.LevelDebug
		case "warn", "warning":
			logLevel = slog.LevelWarn
		case "error":
			logLevel = slog.LevelError
		default:
			logLevel = slog.LevelInfo
		}
		// 解析慢请求阈值
		if t, err := time.ParseDuration(cfg.Middleware.SlowRequestThreshold); err == nil {
			slowThreshold = t
		} else {
			slowThreshold = time.Second
		}
	} else {
		// 默认值
		logLevel = slog.LevelInfo
		slowThreshold = time.Second
	}

	return func(c *gin.Context) {
		atomic.AddInt64(&TotalRequests, 1)
		start := time.Now()
		c.Next()
		latency := time.Since(start)

		if c.Writer.Status() >= 400 {
			atomic.AddInt64(&TotalErrors, 1)
		}
		recordHTTPRequestMetric(c, latency)

		status := c.Writer.Status()
		isError := status >= 400
		isSlow := latency > slowThreshold

		// 根据配置级别和请求状态决定是否记录日志
		shouldLog := false
		currentLevel := logLevel

		// 错误请求始终记录（至少 WARN 级别）
		if isError && logLevel <= slog.LevelWarn {
			shouldLog = true
			if logLevel > slog.LevelWarn {
				currentLevel = slog.LevelWarn
			}
		}

		// 慢请求始终记录
		if isSlow && !shouldLog {
			shouldLog = true
		}

		// 正常请求在 DEBUG 级别时记录
		if !isError && !isSlow && logLevel <= slog.LevelDebug {
			shouldLog = true
			currentLevel = slog.LevelDebug
		}

		if shouldLog {
			// 使用动态日志级别
			slog.Log(c.Request.Context(), currentLevel, "Request handled",
				"method", c.Request.Method,
				"route", metricRoute(c),
				"status", status,
				"latency", latency.String(),
				"client_ip", c.ClientIP(),
			)
		}
	}
}

// RecoveryMiddleware 全局异常捕获
func RecoveryMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if err := recover(); err != nil {
				rid, _ := c.Get(RequestIDKey)
				slog.ErrorContext(c.Request.Context(), "Panic recovered",
					"error_code", "BEAR_RUNTIME_PANIC",
					"category", runtimePanicCategory(err),
					"route", metricRoute(c),
				)
				abortRecoveredResponse(c, rid)
			}
		}()
		c.Next()
	}
}

func abortRecoveredResponse(ctx *gin.Context, rid any) {
	ctx.Abort()
	if ctx.Writer.Written() {
		return
	}
	if ctx.Request != nil && ctx.Request.Method == http.MethodHead {
		ctx.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	ctx.AbortWithStatusJSON(http.StatusInternalServerError, Response{
		Code:    http.StatusInternalServerError,
		Message: fmt.Sprintf("Internal Server Error (RID: %v)", rid),
	})
}
