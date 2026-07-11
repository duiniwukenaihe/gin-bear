package bear

import (
	"fmt"
	"log/slog"
	"runtime/debug"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
)

// HTTPMetrics is the application-scoped HTTP metrics registry.
type HTTPMetrics = httpMetricsRegistry

// Runtime contains state owned by one Bear application.
type Runtime struct {
	Config    *SysConfig
	Logger    *slog.Logger
	Container *BeanFactory
	Lifecycle *Lifecycle
	Metrics   *HTTPMetrics
}

type legacyFacade struct {
	runtime  *Runtime
	injector *BeanFactory
	logger   *slog.Logger
}

var defaultFacade atomic.Pointer[legacyFacade]

const runtimeContextKey = "bear_runtime"

func newRuntime(config *SysConfig) *Runtime {
	lifecycle := newLifecycle()
	container := NewBeanFactory()
	container.onSet = lifecycle.setBean
	container.onRemove = lifecycle.removeBean
	return &Runtime{
		Config:    config,
		Logger:    newLogger(config),
		Container: container,
		Lifecycle: lifecycle,
		Metrics:   newHTTPMetricsRegistry(defaultDurationBuckets),
	}
}

func publishDefaultRuntime(runtime *Runtime) {
	if runtime == nil {
		return
	}
	defaultFacade.Store(&legacyFacade{
		runtime:  runtime,
		injector: runtime.Container,
		logger:   runtime.Logger,
	})
	slog.SetDefault(Log)
}

func loadDefaultFacade() *legacyFacade {
	return defaultFacade.Load()
}

func currentDefaultRuntime() *Runtime {
	facade := loadDefaultFacade()
	if facade == nil {
		return nil
	}
	return facade.runtime
}

func runtimeOwnershipMiddleware(runtime *Runtime) gin.HandlerFunc {
	return func(ctx *gin.Context) {
		ctx.Set(runtimeContextKey, runtime)
		ctx.Next()
	}
}

func updateDefaultFacade(update func(legacyFacade) legacyFacade) *legacyFacade {
	for {
		current := defaultFacade.Load()
		var next legacyFacade
		if current != nil {
			next = *current
		}
		next = update(next)
		nextPointer := &next
		if defaultFacade.CompareAndSwap(current, nextPointer) {
			return nextPointer
		}
	}
}

func runtimePerformanceMiddleware(runtime *Runtime) gin.HandlerFunc {
	config := runtime.Config
	logLevel := slog.LevelInfo
	slowThreshold := time.Second
	if config != nil && config.Middleware != nil {
		logLevel = parseLogLevel(config.Middleware.PerformanceLogLevel)
		if parsed, err := time.ParseDuration(config.Middleware.SlowRequestThreshold); err == nil {
			slowThreshold = parsed
		}
	}

	return func(ctx *gin.Context) {
		atomic.AddInt64(&TotalRequests, 1)
		start := time.Now()
		ctx.Next()
		latency := time.Since(start)
		status := ctx.Writer.Status()
		if status >= 400 {
			atomic.AddInt64(&TotalErrors, 1)
		}
		runtime.Metrics.Record(ctx.Request.Method, metricRoute(ctx), status, latency)

		isError := status >= 400
		isSlow := latency > slowThreshold
		shouldLog := isSlow || (isError && logLevel <= slog.LevelWarn) || (!isError && !isSlow && logLevel <= slog.LevelDebug)
		if !shouldLog {
			return
		}
		currentLevel := logLevel
		if isError && currentLevel < slog.LevelWarn {
			currentLevel = slog.LevelWarn
		}
		runtime.Logger.Log(ctx.Request.Context(), currentLevel, "Request handled",
			"method", ctx.Request.Method,
			"path", ctx.Request.URL.Path,
			"status", status,
			"latency", latency.String(),
			"client_ip", ctx.ClientIP(),
		)
	}
}

func runtimeRecoveryMiddleware(runtime *Runtime) gin.HandlerFunc {
	return func(ctx *gin.Context) {
		defer func() {
			if recovered := recover(); recovered != nil {
				rid, _ := ctx.Get(RequestIDKey)
				runtime.Logger.ErrorContext(ctx.Request.Context(), "Panic recovered",
					"error", recovered,
					"stack_trace", string(debug.Stack()),
				)
				ctx.AbortWithStatusJSON(500, Response{
					Code:    500,
					Message: fmt.Sprintf("Internal Server Error (RID: %v)", rid),
				})
			}
		}()
		ctx.Next()
	}
}

func metricRoute(ctx *gin.Context) string {
	route := ctx.FullPath()
	if route == "" {
		return "unmatched"
	}
	return route
}
