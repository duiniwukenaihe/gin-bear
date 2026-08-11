package bear

import (
	"errors"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	"go.opentelemetry.io/otel/propagation"
	oteltrace "go.opentelemetry.io/otel/trace"
)

// HTTPMetrics is the application-scoped HTTP metrics registry.
type HTTPMetrics = httpMetricsRegistry

// Runtime contains state owned by one Bear application.
type Runtime struct {
	Config            *SysConfig
	Logger            *slog.Logger
	Container         *BeanFactory
	Lifecycle         *Lifecycle
	Metrics           *HTTPMetrics
	TracerProvider    oteltrace.TracerProvider
	TextMapPropagator propagation.TextMapPropagator
	readinessChecks   *readinessCheckCoordinator
	hijackedMu        sync.Mutex
	hijacked          map[io.Closer]struct{}
	hijackedClosing   bool
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
	container.strict = config != nil && config.FrameworkStrict()
	container.onSet = lifecycle.registerBean
	container.onBatchSet = lifecycle.registerBeans
	container.onRemove = lifecycle.removeBean
	runtime := &Runtime{
		Config:          config,
		Logger:          newLogger(config),
		Container:       container,
		Lifecycle:       lifecycle,
		readinessChecks: newReadinessCheckCoordinator(),
		hijacked:        make(map[io.Closer]struct{}),
	}
	if config == nil || config.Metrics == nil || config.Metrics.Enabled {
		runtime.Metrics = newHTTPMetricsRegistry(defaultDurationBuckets)
	}
	return runtime
}

func (r *Runtime) trackHijackedConnection(connection io.Closer) bool {
	if r == nil || connection == nil {
		return false
	}
	r.hijackedMu.Lock()
	if r.hijackedClosing {
		r.hijackedMu.Unlock()
		_ = connection.Close()
		return false
	}
	r.hijacked[connection] = struct{}{}
	r.hijackedMu.Unlock()
	return true
}

func (r *Runtime) beginHijackedShutdown() {
	if r == nil {
		return
	}
	r.hijackedMu.Lock()
	r.hijackedClosing = true
	r.hijackedMu.Unlock()
}

func (r *Runtime) hijackedShutdownStarted() bool {
	if r == nil {
		return true
	}
	r.hijackedMu.Lock()
	closing := r.hijackedClosing
	r.hijackedMu.Unlock()
	return closing
}

func (r *Runtime) untrackHijackedConnection(connection io.Closer) {
	if r == nil || connection == nil {
		return
	}
	r.hijackedMu.Lock()
	delete(r.hijacked, connection)
	r.hijackedMu.Unlock()
}

func (r *Runtime) closeHijackedConnections() error {
	if r == nil {
		return nil
	}
	r.hijackedMu.Lock()
	connections := make([]io.Closer, 0, len(r.hijacked))
	for connection := range r.hijacked {
		connections = append(connections, connection)
		delete(r.hijacked, connection)
	}
	r.hijackedMu.Unlock()

	var closeErrors []error
	for _, connection := range connections {
		if err := connection.Close(); err != nil {
			closeErrors = append(closeErrors, errors.New("hijacked connection close failed"))
		}
	}
	return errors.Join(closeErrors...)
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
		if runtime.Metrics != nil {
			runtime.Metrics.Record(ctx.Request.Method, metricRoute(ctx), status, latency)
		}

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
			"route", metricRoute(ctx),
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
					"error_code", "BEAR_RUNTIME_PANIC",
					"category", runtimePanicCategory(recovered),
					"route", metricRoute(ctx),
				)
				abortRecoveredResponse(ctx, rid)
			}
		}()
		ctx.Next()
	}
}

func runtimePanicCategory(recovered any) string {
	switch recovered.(type) {
	case error:
		return "error_panic"
	case string:
		return "string_panic"
	default:
		return "value_panic"
	}
}

func metricRoute(ctx *gin.Context) string {
	route := ctx.FullPath()
	if route == "" {
		return "unmatched"
	}
	return route
}
