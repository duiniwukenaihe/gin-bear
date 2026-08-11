package bear

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"go.opentelemetry.io/otel/propagation"
	"google.golang.org/grpc"
)

// Bean 接口定义
type Bean interface {
	Name() string
}

// Shutdowner 接口定义，用于组件自动清理
type Shutdowner interface {
	Shutdown() error
}

// Initializer 允许 Bean 在注入后执行初始化逻辑
type Initializer interface {
	Init(ctx context.Context) error
}

// Module 允许按模块组织一组 Bean 和路由
type Module interface {
	Name() string
	Beans() []Bean
	Build(bear *Bear)
}

// ModuleBuilderE lets strict startup report module construction failures.
type ModuleBuilderE interface {
	BuildE(bear *Bear) error
}

// IClass 定义了控制器接口
type IClass interface {
	Bean
	Build(bear *Bear)
}

// ClassBuilderE lets strict startup report controller construction failures.
type ClassBuilderE interface {
	BuildE(bear *Bear) error
}

// ErrBuildRegistrationLoop reports discovery that does not converge within the
// strict startup round limit.
var ErrBuildRegistrationLoop = errors.New("strict build registration did not converge")

type MountMetadata struct {
	Group   string
	Classes []IClass
}

type routeRegistrationContext struct {
	parent     *routeRegistrationContext
	group      *gin.RouterGroup
	groupName  string
	controller IOpenAPI
	fairings   []Fairing
}

var signalNotifyContext = signal.NotifyContext
var ginRuntimeMu sync.Mutex
var strictGinRuntimeMode string

var (
	// ErrAlreadyServing reports a second Serve or Launch call for one Bear.
	ErrAlreadyServing = errors.New("bear is already serving")
	// ErrGinRuntimeConflict reports incompatible strict Gin process modes.
	ErrGinRuntimeConflict = errors.New("strict gin runtime mode conflict")
)

// Bear 是核心框架引擎
type Bear struct {
	*gin.Engine
	g                         *gin.RouterGroup
	exprData                  map[string]interface{}
	fairingHandler            *FairingHandler
	routeTree                 *RouteTree // 路由树，用于存储路由级别的 Fairing
	routeRegistry             []RouteMetadata
	registration              *routeRegistrationContext
	grpcServices              []GRPCService
	mounts                    []MountMetadata
	modules                   []Module
	runtime                   *Runtime
	eRegistrationMu           sync.Mutex
	strictRegistrationVersion uint64
	strictBuiltModules        int
	strictBuiltMounts         int
	strictBuildComplete       bool
	strictPluginModules       map[int]struct{}
	strictInjectionMu         sync.Mutex
	strictInjectionAttempts   map[any]*strictInjectionAttempt
	strictInjectionApplied    map[any]struct{}
	strictInjectionSession    bool
	strictInjectionTargets    []any
	applyMu                   sync.Mutex
	applyState                applyState
	applyErr                  error
	applyAttempt              *applyAttempt
	servingMu                 sync.Mutex
	serving                   bool
	served                    bool
	pluginBarrier             *pluginRegistrationBarrier
	pluginDispatcher          *PluginDispatcher
	pluginManager             *PluginManager
	pluginMode                bool // 标记当前是否处于插件加载模式
	metricsRegistered         atomic.Bool
	tracingRegistered         atomic.Bool
	webSocketRoutes           atomic.Int64
}

type applyState uint8

type applyAttempt struct {
	done chan struct{}
	err  error
}

type strictInjectionAttempt struct {
	done chan struct{}
	err  error
}

const (
	applyNotStarted applyState = iota
	applyRunning
	applySucceeded
	applyFailed
)

// OnShutdown registers cleanup hooks before startup begins. Hooks attempted
// after registration closes are ignored for compatibility.
func (b *Bear) OnShutdown(f ...func()) {
	_ = b.TryOnShutdown(f...)
}

// TryOnShutdown registers cleanup hooks or reports that registration is closed.
func (b *Bear) TryOnShutdown(f ...func()) error {
	if b == nil || b.runtime == nil {
		return nil
	}
	hooks := make([]any, 0, len(f))
	for _, hook := range f {
		if hook != nil {
			hooks = append(hooks, shutdownHook{fn: hook})
		}
	}
	return b.runtime.Lifecycle.add(hooks...)
}

func (b *Bear) Name() string {
	return "Bear"
}

// Runtime returns the application-scoped runtime owned by Bear.
func (b *Bear) Runtime() *Runtime {
	if b == nil {
		return nil
	}
	return b.runtime
}

// Ignite 初始化 Bear 引擎 (轻量级内核)
// 出山 - 象征小白熊破冰而出，准备开始工作
func Ignite(args ...any) *Bear {
	app, err := IgniteE(args...)
	if err != nil {
		panic(err.Error())
	}
	return app
}

// IgniteE initializes the Bear engine and returns startup errors to the caller.
func IgniteE(args ...any) (*Bear, error) {
	var config *SysConfig
	var ginMiddlewares []gin.HandlerFunc

	for _, arg := range args {
		switch v := arg.(type) {
		case *SysConfig:
			config = v
		case gin.HandlerFunc:
			ginMiddlewares = append(ginMiddlewares, v)
		}
	}

	if config == nil {
		loaded, err := LoadConfig()
		if err != nil {
			return nil, err
		}
		config = loaded
	} else {
		config.PostProcess()
	}
	if err := config.Validate(); err != nil {
		return nil, fmt.Errorf("invalid configuration: %w", err)
	}

	if config.DB != nil && config.DB.Enabled && config.DB.DSN == "" && config.DB.DBName == "" {
		return nil, fmt.Errorf("database configuration is required when database.enabled=true (dsn or dbname)")
	}
	if err := validateProductionSecurity(config); err != nil {
		return nil, err
	}
	engine, err := newGinEngine(config)
	if err != nil {
		return nil, err
	}

	runtime := newRuntime(config)
	b := &Bear{
		Engine:           engine,
		exprData:         map[string]interface{}{},
		fairingHandler:   NewFairingHandler(),
		routeTree:        NewRouteTree(),
		pluginBarrier:    newPluginRegistrationBarrier(),
		pluginDispatcher: newPluginDispatcher(runtime.Logger),
		runtime:          runtime,
	}
	b.pluginManager = NewPluginManager(b)

	// 注册核心底座 Bean
	runtime.Container.Set(b)
	runtime.Container.Set(config)
	runtime.Container.Set(newJWTUtilFromAuthConfig(config.Auth))
	for _, warning := range config.compatibilityWarnings() {
		runtime.Logger.Warn(warning)
	}

	// 注入底座中间件
	b.Use(runtimeOwnershipMiddleware(runtime))
	b.Use(securityHeadersMiddleware())
	b.Use(requestBodyLimitMiddleware(effectiveRequestBodyLimit(config)))
	b.Use(RequestIDMiddleware())
	b.Use(runtimePerformanceMiddleware(runtime))
	b.Use(runtimeRecoveryMiddleware(runtime))
	b.Use(b.pluginDispatcher.Dispatch())
	for _, middleware := range ginMiddlewares {
		b.Use(middleware)
	}

	publishDefaultRuntime(runtime)
	runtime.Logger.Info("WhiteBear core awakened", "server", config.Server.Name)
	return b, nil
}

// Deprecated: EnableIDGenerator is compatibility-only and has no effect.
func (b *Bear) EnableIDGenerator() error {
	b.runtime.Logger.Warn("ID Generator is disabled in精简 mode")
	return nil
}

// Deprecated: EnableMQ is compatibility-only and has no effect.
func (b *Bear) EnableMQ(ctx context.Context) *Bear {
	b.runtime.Logger.Warn("MQ is disabled in 精简 mode")
	return b
}

// EnableTracing 开启链路追踪
func (b *Bear) EnableTracing(ctx context.Context) *Bear {
	if err := b.EnableTracingE(ctx); err != nil && b != nil && b.runtime != nil {
		b.runtime.Logger.Error("Tracing initialization failed", "error_code", "BEAR_TRACING_INIT")
	}
	return b
}

// EnableTracingE initializes and registers tracing while registration is open.
func (b *Bear) EnableTracingE(ctx context.Context) error {
	if b == nil || b.runtime == nil {
		return errors.New("bear runtime is unavailable")
	}
	config := b.runtime.Config
	if config == nil || config.Tracing == nil || !config.Tracing.Enabled {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	unlock, err := b.beginGinRegistration()
	if err != nil {
		return err
	}
	defer unlock()
	if !b.tracingRegistered.CompareAndSwap(false, true) {
		return nil
	}
	provider, err := newTracerProvider(ctx, config.Tracing)
	if err != nil {
		b.tracingRegistered.Store(false)
		return err
	}
	if err := b.TryOnShutdown(shutdownTracerProvider(provider)); err != nil {
		shutdownTracerProvider(provider)()
		b.tracingRegistered.Store(false)
		return err
	}
	propagator := propagation.TraceContext{}
	b.runtime.TracerProvider = provider
	b.runtime.TextMapPropagator = propagator
	b.Engine.Use(TracingMiddleware(provider, propagator))
	b.strictRegistrationVersion++
	b.runtime.Logger.Info("Tracing enabled", "exporter", config.Tracing.Exporter, "service", config.Tracing.ServiceName)
	return nil
}

// EnableMetrics 开启指标监控
func (b *Bear) EnableMetrics() *Bear {
	if err := b.EnableMetricsE(); err != nil && b != nil && b.runtime != nil {
		b.runtime.Logger.Error("Metrics registration failed", "error_code", "BEAR_METRICS_REGISTRATION")
	}
	return b
}

// EnableMetricsE registers the metrics endpoint while registration is open.
func (b *Bear) EnableMetricsE() error {
	if b == nil || b.runtime == nil {
		return errors.New("bear runtime is unavailable")
	}
	config := b.runtime.Config
	if config != nil && config.Metrics != nil && !config.Metrics.Enabled {
		return nil
	}
	unlock, err := b.beginGinRegistration()
	if err != nil {
		return err
	}
	defer unlock()
	if b.metricsRegistered.Load() {
		return nil
	}
	if b.runtime.Metrics == nil {
		return errors.New("metrics runtime is unavailable")
	}
	path := "/metrics"
	if config != nil && config.Metrics != nil && config.Metrics.Path != "" {
		path = config.Metrics.Path
	}
	b.activeGroup().GET(path, gin.WrapH(b.runtime.Metrics.Handler()))
	b.metricsRegistered.Store(true)
	b.strictRegistrationVersion++
	return nil
}

// Launch 启动 Bear 引擎，支持优雅退出
// ctx 用于控制启动过程中的超时和取消操作
func (b *Bear) Launch(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	signalCtx, stopSignals := signalNotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stopSignals()
	return b.Serve(signalCtx)
}

// Serve runs the application without installing process signal handlers.
func (b *Bear) Serve(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if !b.acquireServing() {
		return ErrAlreadyServing
	}
	defer b.releaseServing()
	if err := b.ApplyAll(ctx); err != nil {
		if ctx.Err() != nil && isOnlyContextError(err, ctx.Err()) {
			return nil
		}
		return err
	}
	b.markServed()
	config := b.runtime.Config
	logger := b.runtime.Logger
	server := b.buildHTTPServer(config)

	httpListener, err := net.Listen("tcp", server.Addr)
	if err != nil {
		return b.cleanupLaunchFailure(config, fmt.Errorf("failed to listen for HTTP: %w", err))
	}

	var grpcListener net.Listener
	var grpcServer *grpc.Server
	if config.GRPC != nil && config.GRPC.Enabled {
		grpcAddr := fmt.Sprintf(":%d", config.GRPC.Port)
		grpcListener, err = net.Listen("tcp", grpcAddr)
		if err != nil {
			return b.cleanupLaunchFailure(config, fmt.Errorf("failed to listen for gRPC: %w", err), httpListener)
		}
		grpcServer = grpc.NewServer()
		for _, service := range b.grpcServices {
			logger.Info("Registering gRPC service", "name", service.Name())
			service.Register(grpcServer)
		}
	}

	type serveResult struct {
		name string
		err  error
	}
	serverCount := 1
	if grpcServer != nil {
		serverCount++
	}
	serveResults := make(chan serveResult, serverCount)
	go func() {
		logger.Info("WhiteBear is emerging from ice", "addr", httpListener.Addr().String(), "name", config.Server.Name)
		err := server.Serve(httpListener)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		serveResults <- serveResult{name: "HTTP", err: err}
	}()
	if grpcServer != nil {
		go func() {
			logger.Info("WhiteBear gRPC is listening", "addr", grpcListener.Addr().String())
			err := grpcServer.Serve(grpcListener)
			if errors.Is(err, grpc.ErrServerStopped) {
				err = nil
			}
			serveResults <- serveResult{name: "gRPC", err: err}
		}()
	}

	var launchErrors []error
	received := 0
	select {
	case result := <-serveResults:
		received++
		if result.err != nil {
			launchErrors = append(launchErrors, fmt.Errorf("%s serve failed: %w", result.name, result.err))
		}
	case <-ctx.Done():
		logger.Info("Context cancelled, shutting down...")
	}

	shutdownBudget := shutdownTimeout(config)
	if err := runShutdownPhase(shutdownBudget, func(ctx context.Context) error {
		b.runtime.beginHijackedShutdown()
		serverErr := shutdownHTTPServer(ctx, server)
		connectionsErr := b.runtime.closeHijackedConnections()
		return errors.Join(serverErr, connectionsErr)
	}); err != nil {
		launchErrors = append(launchErrors, err)
	}
	if grpcServer != nil {
		if err := runShutdownPhase(shutdownBudget, func(ctx context.Context) error {
			return shutdownGRPCServer(ctx, grpcServer)
		}); err != nil {
			launchErrors = append(launchErrors, err)
		}
	}
	if err := runShutdownPhase(shutdownBudget, b.runtime.Lifecycle.Stop); err != nil {
		launchErrors = append(launchErrors, err)
	}

	if err := runShutdownPhase(shutdownBudget, func(ctx context.Context) error {
		var waitErrors []error
		for received < serverCount {
			select {
			case result := <-serveResults:
				received++
				if result.err != nil {
					waitErrors = append(waitErrors, fmt.Errorf("%s serve failed: %w", result.name, result.err))
				}
			case <-ctx.Done():
				return errors.Join(errors.Join(waitErrors...), fmt.Errorf("waiting for servers to stop: %w", ctx.Err()))
			}
		}
		return errors.Join(waitErrors...)
	}); err != nil {
		launchErrors = append(launchErrors, err)
	}

	logger.Info("WhiteBear returning to ice")
	return errors.Join(launchErrors...)
}

func isOnlyContextError(err, contextErr error) bool {
	if err == nil || contextErr == nil {
		return false
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		children := joined.Unwrap()
		if len(children) == 0 {
			return false
		}
		for _, child := range children {
			if !isOnlyContextError(child, contextErr) {
				return false
			}
		}
		return true
	}
	if wrapped, ok := err.(interface{ Unwrap() error }); ok {
		if child := wrapped.Unwrap(); child != nil {
			return isOnlyContextError(child, contextErr)
		}
	}
	return errors.Is(err, contextErr)
}

func (b *Bear) acquireServing() bool {
	if b == nil {
		return false
	}
	b.servingMu.Lock()
	defer b.servingMu.Unlock()
	if b.serving || b.served {
		return false
	}
	b.serving = true
	return true
}

func (b *Bear) markServed() {
	if b == nil {
		return
	}
	b.servingMu.Lock()
	b.served = true
	b.servingMu.Unlock()
}

func (b *Bear) releaseServing() {
	if b == nil {
		return
	}
	b.servingMu.Lock()
	b.serving = false
	b.servingMu.Unlock()
}

func (b *Bear) cleanupLaunchFailure(config *SysConfig, cause error, listeners ...net.Listener) error {
	errorsToJoin := []error{cause}
	for _, listener := range listeners {
		errorsToJoin = append(errorsToJoin, closeListener("pre-bound", listener))
	}
	errorsToJoin = append(errorsToJoin, runShutdownPhase(shutdownTimeout(config), b.runtime.Lifecycle.Stop))
	return errors.Join(errorsToJoin...)
}

func runShutdownPhase(timeout time.Duration, phase func(context.Context) error) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return runShutdownWorker(ctx, func() error { return phase(ctx) })
}

func closeListener(name string, listener net.Listener) error {
	if listener == nil {
		return nil
	}
	if err := listener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
		return fmt.Errorf("close %s listener: %w", name, err)
	}
	return nil
}

func shutdownHTTPServer(ctx context.Context, server *http.Server) error {
	if err := server.Shutdown(ctx); err != nil {
		return errors.Join(fmt.Errorf("HTTP shutdown: %w", err), server.Close())
	}
	return nil
}

type grpcShutdownServer interface {
	GracefulStop()
	Stop()
}

func shutdownGRPCServer(ctx context.Context, server grpcShutdownServer) error {
	done := make(chan struct{})
	go func() {
		server.GracefulStop()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		server.Stop()
		return fmt.Errorf("gRPC graceful shutdown: %w", ctx.Err())
	}
}

func (b *Bear) buildHTTPServer(config *SysConfig) *http.Server {
	port := 8080
	if config != nil && config.Server != nil && config.Server.Port > 0 {
		port = int(config.Server.Port)
	}

	srv := &http.Server{
		Addr:              fmt.Sprintf(":%d", port),
		Handler:           b.Engine,
		ReadHeaderTimeout: parseDurationOrDefault(configDuration(config, "read_header_timeout"), 5*time.Second),
		ReadTimeout:       parseDurationOrDefault(configDuration(config, "read_timeout"), 15*time.Second),
		WriteTimeout:      parseDurationOrDefault(configDuration(config, "write_timeout"), 30*time.Second),
		IdleTimeout:       parseDurationOrDefault(configDuration(config, "idle_timeout"), 60*time.Second),
		MaxHeaderBytes:    defaultHTTPSizeLimit,
	}
	if config != nil && config.Server != nil && config.Server.MaxHeaderBytes > 0 {
		srv.MaxHeaderBytes = config.Server.MaxHeaderBytes
	}
	return srv
}

func configDuration(config *SysConfig, key string) string {
	if config == nil || config.Server == nil {
		return ""
	}
	switch key {
	case "read_header_timeout":
		return config.Server.ReadHeaderTimeout
	case "read_timeout":
		return config.Server.ReadTimeout
	case "write_timeout":
		return config.Server.WriteTimeout
	case "idle_timeout":
		return config.Server.IdleTimeout
	default:
		return ""
	}
}

func parseDurationOrDefault(raw string, fallback time.Duration) time.Duration {
	if raw == "" {
		return fallback
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return fallback
	}
	return d
}

func shutdownTimeout(config *SysConfig) time.Duration {
	if config == nil || config.Server == nil {
		return 5 * time.Second
	}
	return parseDurationOrDefault(config.Server.ShutdownTimeout, 5*time.Second)
}

func newGinEngine(config *SysConfig) (engine *gin.Engine, err error) {
	ginRuntimeMu.Lock()
	defer ginRuntimeMu.Unlock()
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("construct gin engine: %v", recovered)
		}
	}()
	mode := configuredGinMode(config)
	if strictGinRuntimeMode != "" && strictGinRuntimeMode != mode {
		return nil, fmt.Errorf("%w: active=%s requested=%s", ErrGinRuntimeConflict, strictGinRuntimeMode, mode)
	}
	previousMode := gin.Mode()
	previousWriter := gin.DefaultWriter
	previousErrorWriter := gin.DefaultErrorWriter
	committed := false
	defer func() {
		if committed {
			return
		}
		gin.SetMode(previousMode)
		gin.DefaultWriter = previousWriter
		gin.DefaultErrorWriter = previousErrorWriter
	}()
	gin.SetMode(mode)
	gin.DefaultWriter = io.Discard
	gin.DefaultErrorWriter = os.Stderr
	engine = gin.New()
	if config != nil && config.Server != nil {
		if err := engine.SetTrustedProxies(config.Server.TrustedProxies); err != nil {
			return nil, fmt.Errorf("invalid trusted proxies: %w", err)
		}
	}
	if config != nil && config.FrameworkStrict() && strictGinRuntimeMode == "" {
		strictGinRuntimeMode = mode
	}
	committed = true
	return engine, nil
}

func configuredGinMode(config *SysConfig) string {
	if config != nil && config.Server != nil && config.Server.Mode != "" {
		return config.Server.Mode
	}
	if mode := os.Getenv("GIN_MODE"); mode != "" {
		return mode
	}
	if isProductionEnvironment(os.Getenv("BEAR_ENV")) {
		return gin.ReleaseMode
	}
	return gin.DebugMode
}

func isProductionMode(config *SysConfig) bool {
	mode := configuredGinMode(config)
	if mode == gin.ReleaseMode {
		return true
	}
	return isProductionEnvironment(os.Getenv("BEAR_ENV"))
}

func validateProductionSecurity(config *SysConfig) error {
	if err := validateCORSConfig(config); err != nil {
		return err
	}
	if !isProductionMode(config) {
		return nil
	}
	if config == nil {
		return nil
	}
	if err := validateProductionTrustedProxies(config); err != nil {
		return err
	}
	if config.Auth != nil {
		if isWeakProductionJWTSecret(config.Auth.JWTSecret) {
			return fmt.Errorf("weak jwt secret is not allowed in production")
		}
	}
	if err := validateProductionTimeouts(config); err != nil {
		return err
	}
	if err := validateProductionWebSocketPolicy(config); err != nil {
		return err
	}
	if config.WS != nil && !config.WS.CheckOrigin && len(config.WS.GetAllowedOrigins()) == 0 {
		return fmt.Errorf("websocket origin check cannot be disabled in production without allowed origins")
	}
	return nil
}

func validateProductionTimeouts(config *SysConfig) error {
	if config == nil {
		return nil
	}
	timeouts := []struct {
		name string
		raw  string
		max  time.Duration
	}{
		{name: "server.read_header_timeout", raw: configDuration(config, "read_header_timeout"), max: 30 * time.Second},
		{name: "server.read_timeout", raw: configDuration(config, "read_timeout"), max: 5 * time.Minute},
		{name: "server.write_timeout", raw: configDuration(config, "write_timeout"), max: 5 * time.Minute},
		{name: "server.idle_timeout", raw: configDuration(config, "idle_timeout"), max: 10 * time.Minute},
	}
	if config.Server != nil {
		timeouts = append(timeouts, struct {
			name string
			raw  string
			max  time.Duration
		}{name: "server.shutdown_timeout", raw: config.Server.ShutdownTimeout, max: time.Minute})
	}
	if config.Health != nil {
		timeouts = append(timeouts, struct {
			name string
			raw  string
			max  time.Duration
		}{name: "health.readiness_timeout", raw: config.Health.ReadinessTimeout, max: time.Minute})
	}
	if config.Middleware != nil {
		timeouts = append(timeouts, struct {
			name string
			raw  string
			max  time.Duration
		}{name: "middleware.slow_request_threshold", raw: config.Middleware.SlowRequestThreshold, max: 5 * time.Minute})
	}
	for _, timeout := range timeouts {
		if strings.TrimSpace(timeout.raw) == "" {
			continue
		}
		value, err := time.ParseDuration(timeout.raw)
		if err != nil {
			return fmt.Errorf("%s timeout is invalid: %w", timeout.name, err)
		}
		if value <= 0 || value > timeout.max {
			return fmt.Errorf("%s timeout must be positive and at most %s", timeout.name, timeout.max)
		}
	}
	return nil
}

// Mount 挂载控制器
func (b *Bear) Mount(group string, classes ...IClass) *Bear {
	if b.frameworkStrict() {
		if err := b.MountE(group, classes...); err != nil {
			panic(err)
		}
		return b
	}
	b.mounts = append(b.mounts, MountMetadata{Group: group, Classes: classes})
	for _, class := range classes {
		b.Beans(class)
	}
	return b
}

// MountE registers controllers and their metadata only when strict bean
// registration succeeds.
func (b *Bear) MountE(group string, classes ...IClass) error {
	if b == nil || b.runtime == nil {
		return errors.New("bear runtime is unavailable")
	}
	beans := make([]Bean, 0, len(classes))
	for _, class := range classes {
		beans = append(beans, class)
	}
	values, names, err := prepareStrictBeans(beans)
	if err != nil {
		return fmt.Errorf("register mounted controllers: %w", err)
	}
	b.eRegistrationMu.Lock()
	defer b.eRegistrationMu.Unlock()
	if err := b.runtime.Container.trySetBatchStrict(values); err != nil {
		return fmt.Errorf("register mounted controllers: %w", err)
	}
	publishBeanMetadata(b.exprData, beans, names)
	b.mounts = append(b.mounts, MountMetadata{Group: group, Classes: classes})
	b.strictRegistrationVersion++
	return nil
}

// EnableHealth 启用健康检查与指标端点
func (b *Bear) EnableHealth() *Bear {
	if err := b.EnableHealthE(); err != nil {
		if b != nil && b.runtime != nil {
			b.runtime.Logger.Error("Health registration failed", "error_code", "BEAR_HEALTH_REGISTRATION")
		}
		return b
	}
	config := b.runtime.Config
	if config == nil || config.Metrics == nil || config.Metrics.Enabled {
		if err := b.EnableMetricsE(); err != nil {
			b.runtime.Logger.Error("Metrics registration failed", "error_code", "BEAR_METRICS_REGISTRATION")
		}
	}
	return b
}

// EnableHealthE registers health endpoints while registration is open.
func (b *Bear) EnableHealthE() error {
	if b == nil || b.runtime == nil {
		return errors.New("bear runtime is unavailable")
	}
	return b.MountE("", &HealthController{runtime: b.runtime})
}

// EnableDatabase opens and registers the configured database adapter.
func (b *Bear) EnableDatabase(ctx context.Context) *Bear {
	if err := b.EnableDatabaseE(ctx); err != nil && b != nil && b.runtime != nil {
		b.runtime.Logger.Error("Database initialization failed", "error_code", "BEAR_DATABASE_INIT")
	}
	return b
}

// EnableDatabaseE opens, verifies, and registers the configured database.
func (b *Bear) EnableDatabaseE(ctx context.Context) error {
	if b == nil || b.runtime == nil {
		return errors.New("bear runtime is unavailable")
	}
	config := b.runtime.Config
	if config == nil || config.DB == nil || !config.DB.Enabled {
		return nil
	}
	unlock, err := b.beginGinRegistration()
	if err != nil {
		return err
	}
	unlock()
	if ctx == nil {
		ctx = context.Background()
	}
	adapter, err := NewGormAdapter(config.DB)
	if err != nil {
		return err
	}
	checkCtx, cancel := context.WithTimeout(ctx, readinessTimeout(config))
	err = adapter.CheckReady(checkCtx)
	cancel()
	if err != nil {
		_ = adapter.Shutdown()
		return fmt.Errorf("database readiness check failed: %w", err)
	}
	if err := b.BeansE(adapter); err != nil {
		_ = adapter.Shutdown()
		return fmt.Errorf("register database adapter: %w", err)
	}
	return nil
}

// EnableGzip 启用 Gzip 响应压缩 (阶段 84)
func (b *Bear) EnableGzip(minLength ...int) *Bear {
	if err := b.EnableGzipE(minLength...); err != nil {
		panic(err)
	}
	return b
}

// EnableGzipE registers Gzip middleware while strict registration is open.
func (b *Bear) EnableGzipE(minLength ...int) error {
	limit := 1024
	if len(minLength) > 0 {
		limit = minLength[0]
	}
	return b.UseE(GzipMiddleware(limit))
}

// Beans 注册 Bean
func (b *Bear) Beans(beans ...Bean) *Bear {
	if b.frameworkStrict() {
		if err := b.BeansE(beans...); err != nil {
			panic(err)
		}
		return b
	}
	for _, bean := range beans {
		b.exprData[bean.Name()] = bean
		b.runtime.Container.Set(bean)
	}
	return b
}

// BeansE registers beans while returning lifecycle and strict IoC errors.
func (b *Bear) BeansE(beans ...Bean) error {
	if b == nil || b.runtime == nil {
		return errors.New("bear runtime is unavailable")
	}
	values, names, err := prepareStrictBeans(beans)
	if err != nil {
		return err
	}
	b.eRegistrationMu.Lock()
	defer b.eRegistrationMu.Unlock()
	if err := b.runtime.Container.trySetBatchStrict(values); err != nil {
		return fmt.Errorf("register beans: %w", err)
	}
	publishBeanMetadata(b.exprData, beans, names)
	b.strictRegistrationVersion++
	return nil
}

func prepareStrictBeans(beans []Bean) ([]any, []string, error) {
	values := make([]any, len(beans))
	for index, bean := range beans {
		if bean == nil || isNilBean(bean) {
			return nil, nil, fmt.Errorf("bean item %d (%T) must not be nil", index, bean)
		}
		values[index] = bean
	}
	names := make([]string, len(beans))
	for index, bean := range beans {
		names[index] = bean.Name()
	}
	return values, names, nil
}

func publishBeanMetadata(metadata map[string]interface{}, beans []Bean, names []string) {
	for index, bean := range beans {
		metadata[names[index]] = bean
	}
}

// Attach 注册全局 Fairing
func (b *Bear) Attach(f ...Fairing) *Bear {
	if b.frameworkStrict() {
		if err := b.AttachE(f...); err != nil {
			panic(err)
		}
		return b
	}
	b.fairingHandler.AddFairing(f...)
	for _, f1 := range f {
		b.runtime.Container.Set(f1)
	}
	return b
}

// AttachE registers all Fairings before publishing them to the request path.
func (b *Bear) AttachE(fairings ...Fairing) error {
	if b == nil || b.runtime == nil {
		return errors.New("bear runtime is unavailable")
	}
	values := make([]any, len(fairings))
	for index, fairing := range fairings {
		if fairing == nil || isNilBean(fairing) {
			return fmt.Errorf("fairing item %d (%T) must not be nil", index, fairing)
		}
		values[index] = fairing
	}
	b.eRegistrationMu.Lock()
	defer b.eRegistrationMu.Unlock()
	if err := b.runtime.Container.trySetBatchStrict(values); err != nil {
		return fmt.Errorf("register fairings: %w", err)
	}
	b.fairingHandler.AddFairing(fairings...)
	b.strictRegistrationVersion++
	return nil
}

// LoadPlugin 动态加载 .so 插件 (阶段 85)
func (b *Bear) LoadPlugin(path string) error {
	return b.pluginManager.Load(path)
}

// ReloadPlugin 热更新 .so 插件 (阶段 85)
func (b *Bear) ReloadPlugin(path string) error {
	return b.pluginManager.Reload(path)
}

// AddModule 注册模块
func (b *Bear) AddModule(modules ...Module) *Bear {
	if b.frameworkStrict() {
		if err := b.AddModuleE(modules...); err != nil {
			panic(err)
		}
		return b
	}
	for _, mod := range modules {
		b.runtime.Logger.Info("Loading module", "name", mod.Name())
		// 1. 注册模块中的 Beans
		b.Beans(mod.Beans()...)
		// 2. 暂存模块
		b.modules = append(b.modules, mod)
	}
	return b
}

// AddModuleE registers module beans before publishing the module metadata.
func (b *Bear) AddModuleE(modules ...Module) error {
	return b.addModulesE(false, modules...)
}

func (b *Bear) addModulesE(pluginModules bool, modules ...Module) error {
	if b == nil || b.runtime == nil {
		return errors.New("bear runtime is unavailable")
	}
	for index, mod := range modules {
		if mod == nil || isNilBean(mod) {
			return fmt.Errorf("module item %d (%T) must not be nil", index, mod)
		}
	}
	moduleNames := make([]string, len(modules))
	beans := make([]Bean, 0)
	for index, mod := range modules {
		moduleNames[index] = mod.Name()
		beans = append(beans, mod.Beans()...)
	}
	values, beanNames, err := prepareStrictBeans(beans)
	if err != nil {
		return fmt.Errorf("register modules: %w", err)
	}
	b.eRegistrationMu.Lock()
	defer b.eRegistrationMu.Unlock()
	if err := b.runtime.Container.trySetBatchStrict(values); err != nil {
		return fmt.Errorf("register modules: %w", err)
	}
	publishBeanMetadata(b.exprData, beans, beanNames)
	for index, mod := range modules {
		b.runtime.Logger.Info("Loading module", "name", moduleNames[index])
		b.modules = append(b.modules, mod)
		if pluginModules {
			if b.strictPluginModules == nil {
				b.strictPluginModules = make(map[int]struct{})
			}
			b.strictPluginModules[len(b.modules)-1] = struct{}{}
		}
	}
	b.strictRegistrationVersion++
	return nil
}

// HandleWS 注册 WebSocket 路由
func (b *Bear) HandleWS(relativePath string, handler WebSocketHandler) *Bear {
	if err := b.HandleWSE(relativePath, handler); err != nil {
		panic(err)
	}
	return b
}

// HandleWSE registers a WebSocket route while strict registration is open.
func (b *Bear) HandleWSE(relativePath string, handler WebSocketHandler) error {
	unlock, err := b.beginGinRegistration()
	if err != nil {
		return err
	}
	defer unlock()
	// 1. 执行依赖注入
	if b.frameworkStrict() {
		b.strictInjectionTargets = append(b.strictInjectionTargets, handler)
	} else {
		b.runtime.Container.Apply(handler)
	}

	b.activeGroup().GET(relativePath, func(ctx *gin.Context) {
		// 2. 触发 Fairing OnRequest (支持鉴权、限流等)
		if err := b.runWebSocketRequestFairings(ctx); err != nil {
			WriteError(ctx, err)
			return
		}
		if requestFairingTerminal(ctx) {
			return
		}
		if b.runtime.hijackedShutdownStarted() {
			ctx.AbortWithStatus(http.StatusServiceUnavailable)
			return
		}

		// 3. 获取 WS 配置并初始化升级程序
		config := b.runtime.Config
		policy := webSocketPolicyForConfig(config)
		if !b.runtime.acquireWebSocketConnection(policy.maxConnections) {
			WriteError(ctx, NewStatusError(
				http.StatusServiceUnavailable,
				http.StatusServiceUnavailable,
				"websocket connection limit reached",
				nil,
			))
			return
		}
		defer b.runtime.releaseWebSocketConnection(policy.maxConnections)
		var wsConfig *WebSocketConfig
		if config != nil {
			wsConfig = config.WS
		}
		handshakeTimeout := 10 * time.Second
		readBufferSize := 0
		writeBufferSize := 0
		if wsConfig != nil {
			if wsConfig.HandshakeTimeout > 0 {
				handshakeTimeout = time.Duration(wsConfig.HandshakeTimeout) * time.Millisecond
			}
			readBufferSize = wsConfig.ReadBufferSize
			writeBufferSize = wsConfig.WriteBufferSize
		}
		upgrader := websocket.Upgrader{
			HandshakeTimeout: handshakeTimeout,
			ReadBufferSize:   readBufferSize,
			WriteBufferSize:  writeBufferSize,
			CheckOrigin: func(r *http.Request) bool {
				return websocketOriginAllowed(config, r)
			},
		}

		// 4. 升级协议
		conn, err := upgrader.Upgrade(ctx.Writer, ctx.Request, nil)
		if err != nil {
			b.runtime.Logger.ErrorContext(ctx.Request.Context(), "WebSocket upgrade failed", "error_code", "BEAR_WS_UPGRADE")
			return
		}
		if !b.runtime.trackHijackedConnection(conn) {
			b.runtime.Logger.InfoContext(ctx.Request.Context(), "WebSocket rejected during shutdown", "error_code", "BEAR_WS_SHUTTING_DOWN")
			return
		}
		defer b.runtime.untrackHijackedConnection(conn)
		defer conn.Close()
		conn.SetReadLimit(policy.maxMessageBytes)
		if err := conn.SetReadDeadline(time.Now().Add(policy.readTimeout)); err != nil {
			b.runtime.Logger.ErrorContext(ctx.Request.Context(), "WebSocket deadline setup failed", "error_code", "BEAR_WS_DEADLINE")
			return
		}
		conn.SetPongHandler(func(string) error {
			return conn.SetReadDeadline(time.Now().Add(policy.readTimeout))
		})
		stopHeartbeat := startWebSocketHeartbeat(conn, policy)
		defer stopHeartbeat()

		// 5. 调用 OnConnect
		_ = conn.SetWriteDeadline(time.Now().Add(policy.writeTimeout))
		if err := handler.OnConnect(ctx, conn); err != nil {
			b.runtime.Logger.ErrorContext(ctx.Request.Context(), "WebSocket OnConnect failed", "error_code", "BEAR_WS_CONNECT")
			return
		}
		defer handler.OnClose(ctx, conn)

		// 6. 消息处理循环
		for {
			messageType, p, err := conn.ReadMessage()
			if err != nil {
				b.runtime.Logger.InfoContext(ctx.Request.Context(), "WebSocket connection closed", "category", "connection_closed")
				break
			}
			_ = conn.SetWriteDeadline(time.Now().Add(policy.writeTimeout))
			if err := handler.OnMessage(ctx, conn, messageType, p); err != nil {
				b.runtime.Logger.ErrorContext(ctx.Request.Context(), "WebSocket OnMessage error", "error_code", "BEAR_WS_MESSAGE")
			}
		}
	})
	b.webSocketRoutes.Add(1)
	b.strictRegistrationVersion++
	return nil
}

// Handle registers a compiled business handler and panics if its signature is
// invalid. A standard gin.HandlerFunc is supported as an opaque response
// writer: request Fairings run, but response Fairings cannot transform bytes it
// writes directly. Use HandleE to receive construction errors instead.
func (b *Bear) Handle(httpMethod, relativePath string, handler interface{}) *Bear {
	if err := b.HandleE(httpMethod, relativePath, handler); err != nil {
		panic(err)
	}
	return b
}

// HandleE registers a handler after validating and compiling its concrete
// function value. It returns before mutating route state when construction
// fails.
func (b *Bear) HandleE(httpMethod, relativePath string, handler interface{}) error {
	return b.registerHandler(httpMethod, relativePath, handler, nil)
}

// HandleWithFairing 注册路由并绑定路由级别的 Fairing
// 路由级别的 Fairing 会在全局 Fairing 之前执行（OnRequest）
// 在全局 Fairing 之后执行（OnResponse）
func (b *Bear) HandleWithFairing(httpMethod, relativePath string, handler interface{}, fairings ...Fairing) *Bear {
	if err := b.HandleWithFairingE(httpMethod, relativePath, handler, fairings...); err != nil {
		panic(err)
	}
	return b
}

// HandleWithFairingE registers a route and its Fairings as one strict mutation.
func (b *Bear) HandleWithFairingE(httpMethod, relativePath string, handler interface{}, fairings ...Fairing) error {
	unlock, err := b.beginGinRegistration()
	if err != nil {
		return err
	}
	defer unlock()
	wrappedHandler, err := b.compilePipeline(handler, fairings)
	if err != nil {
		return err
	}

	for _, f := range fairings {
		if b.frameworkStrict() {
			b.strictInjectionTargets = append(b.strictInjectionTargets, f)
		} else {
			b.runtime.Container.Apply(f)
		}
	}
	b.routeTree.addRoute(httpMethod, relativePath, fairings)
	b.registerCompiledHandler(httpMethod, relativePath, handler, wrappedHandler, fairings)
	b.strictRegistrationVersion++
	return nil
}

func (b *Bear) registerHandler(httpMethod, relativePath string, handler interface{}, routeFairings []Fairing) error {
	unlock, err := b.beginGinRegistration()
	if err != nil {
		return err
	}
	defer unlock()
	wrappedHandler, err := b.compilePipeline(handler, routeFairings)
	if err != nil {
		return err
	}
	b.registerCompiledHandler(httpMethod, relativePath, handler, wrappedHandler, routeFairings)
	b.strictRegistrationVersion++
	return nil
}

func (b *Bear) registerCompiledHandler(httpMethod, relativePath string, handler interface{}, wrapped gin.HandlerFunc, routeFairings []Fairing) {
	if b.pluginMode {
		b.pluginDispatcher.Register(httpMethod, relativePath, wrapped)
		return
	}
	group := b.activeGroup()
	group.Handle(httpMethod, relativePath, wrapped)
	registration := b.registration
	groupName := ""
	var controller IOpenAPI
	var controllerFairings []Fairing
	if registration != nil {
		groupName = registration.groupName
		controller = registration.controller
		controllerFairings = registration.fairings
	}
	route := RouteMetadata{
		Method:      httpMethod,
		Path:        relativePath,
		GroupName:   groupName,
		HandlerType: reflect.TypeOf(handler),
		HandlerName: runtimeFuncName(handler),
	}
	b.routeRegistry = append(b.routeRegistry, route)
	effectiveFairings := append([]Fairing(nil), controllerFairings...)
	effectiveFairings = append(effectiveFairings, routeFairings...)
	b.setOpenAPIRouteMetadata(route, joinRoutePath(group.BasePath(), relativePath), controller, effectiveFairings...)
}

func joinRoutePath(basePath, relativePath string) string {
	if basePath == "/" {
		basePath = ""
	}
	return "/" + strings.TrimPrefix(strings.TrimSuffix(basePath, "/")+"/"+strings.TrimPrefix(relativePath, "/"), "/")
}

func (b *Bear) compilePipeline(handler interface{}, routeFairings []Fairing) (gin.HandlerFunc, error) {
	if opaque, ok := opaqueGinHandler(handler); ok {
		return func(ctx *gin.Context) {
			if err := b.runPipelineRequestFairings(ctx, routeFairings); err != nil {
				WriteError(ctx, err)
				return
			}
			if requestFairingTerminal(ctx) {
				return
			}
			opaque(ctx)
		}, nil
	}

	compiled, err := compileHandler(handler)
	if err != nil {
		return nil, err
	}
	return func(ctx *gin.Context) {
		if err := b.runPipelineRequestFairings(ctx, routeFairings); err != nil {
			WriteError(ctx, err)
			return
		}
		if requestFairingTerminal(ctx) {
			return
		}

		result, err := compiled(ctx)
		if err != nil {
			WriteError(ctx, err)
			return
		}
		if requestFairingTerminal(ctx) {
			return
		}

		result, err = b.runPipelineResponseFairings(ctx, result, routeFairings)
		if err != nil {
			WriteError(ctx, err)
			return
		}
		writeSuccess(ctx, result)
	}, nil
}

func (b *Bear) runRequestFairings(ctx *gin.Context, routeFairings []Fairing) error {
	if err := runRequestFairings(ctx, routeFairings); err != nil {
		return err
	}
	if requestFairingTerminal(ctx) {
		return nil
	}
	return b.fairingHandler.OnRequest(ctx)
}

func (b *Bear) frameworkStrict() bool {
	return b != nil && b.runtime != nil && b.runtime.Config != nil && b.runtime.Config.FrameworkStrict()
}

func (b *Bear) beginGinRegistration() (func(), error) {
	if b == nil || b.runtime == nil || b.Engine == nil {
		return nil, errors.New("bear runtime is unavailable")
	}
	b.eRegistrationMu.Lock()
	if b.frameworkStrict() && b.runtime.Lifecycle.registrationClosed() {
		b.eRegistrationMu.Unlock()
		return nil, ErrLifecycleRegistrationClosed
	}
	return b.eRegistrationMu.Unlock, nil
}

// UseE registers global Gin middleware while strict registration is open.
func (b *Bear) UseE(middleware ...gin.HandlerFunc) error {
	unlock, err := b.beginGinRegistration()
	if err != nil {
		return err
	}
	defer unlock()
	b.Engine.Use(middleware...)
	b.strictRegistrationVersion++
	return nil
}

func (b *Bear) runStrictGlobalFairings(ctx *gin.Context, state *strictFairingState) error {
	if state == nil || requestFairingTerminal(ctx) {
		return nil
	}
	if state.globalStarted {
		return nil
	}
	state.globalStarted = true
	return runEnteredRequestFairings(ctx, state, b.fairingHandler.requestFairings)
}

func (b *Bear) runPipelineRequestFairings(ctx *gin.Context, routeFairings []Fairing) error {
	if !b.frameworkStrict() {
		return b.runRequestFairings(ctx, routeFairings)
	}
	if requestFairingTerminal(ctx) {
		return nil
	}
	state := strictFairingStateFor(ctx)
	if err := b.runStrictGlobalFairings(ctx, state); err != nil {
		return err
	}
	if requestFairingTerminal(ctx) {
		return nil
	}
	return runEnteredRequestFairings(ctx, state, routeFairings)
}

func (b *Bear) runPipelineResponseFairings(ctx *gin.Context, result any, routeFairings []Fairing) (any, error) {
	if !b.frameworkStrict() {
		response, err := b.fairingHandler.OnResponseE(result)
		if err != nil {
			return nil, err
		}
		return runResponseFairings(routeFairings, response)
	}
	if ctx == nil {
		return result, nil
	}
	return runEnteredResponseFairings(strictFairingStateFor(ctx), result)
}

func (b *Bear) runWebSocketRequestFairings(ctx *gin.Context) error {
	if !b.frameworkStrict() {
		return b.fairingHandler.OnRequest(ctx)
	}
	state := strictFairingStateFor(ctx)
	return b.runStrictGlobalFairings(ctx, state)
}

func runResponseFairings(fairings []Fairing, result any) (any, error) {
	response := result
	for _, fairing := range fairings {
		transformed, err := fairing.OnResponse(response)
		if err != nil {
			return nil, err
		}
		response = transformed
	}
	return response, nil
}

func (b *Bear) activeGroup() *gin.RouterGroup {
	if b.registration != nil {
		return b.registration.group
	}
	if b.g != nil {
		return b.g
	}
	return &b.Engine.RouterGroup
}

func websocketOriginAllowed(config *SysConfig, r *http.Request) bool {
	if config == nil {
		return true
	}
	origin := r.Header.Get("Origin")
	if config.WS == nil {
		return !isProductionMode(config) || origin == "" || origin == "http://"+r.Host || origin == "https://"+r.Host
	}
	allowedOrigins := config.WS.GetAllowedOrigins()
	if len(allowedOrigins) > 0 {
		return origin == "" || slices.Contains(allowedOrigins, origin) || slices.Contains(allowedOrigins, "*")
	}
	if !config.WS.CheckOrigin {
		return true
	}
	return origin == "" || origin == "http://"+r.Host || origin == "https://"+r.Host
}

func runtimeFuncName(i interface{}) string {
	return reflect.TypeOf(i).String()
}

// ApplyAll 应用依赖注入并执行初始化
// ctx 用于控制初始化过程中的超时和取消操作
func (b *Bear) ApplyAll(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err, cached := b.cachedApplyResult(); cached {
		return err
	}
	if err := b.closePluginRegistration(ctx); err != nil {
		if cachedErr, cached := b.cachedApplyResult(); cached {
			return cachedErr
		}
		return err
	}
	b.applyMu.Lock()
	switch b.applyState {
	case applySucceeded:
		b.applyMu.Unlock()
		return nil
	case applyFailed:
		err := b.applyErr
		b.applyMu.Unlock()
		return err
	case applyRunning:
		attempt := b.applyAttempt
		b.applyMu.Unlock()
		select {
		case <-attempt.done:
			return attempt.err
		case <-ctx.Done():
			return ctx.Err()
		}
	default:
		b.applyState = applyRunning
		b.applyErr = nil
		b.applyAttempt = &applyAttempt{done: make(chan struct{})}
		b.applyMu.Unlock()
	}

	err := b.applyAll(ctx)
	b.applyMu.Lock()
	if err != nil {
		if b.frameworkStrict() && b.runtime.Lifecycle.canRetryStart() {
			b.applyState = applyNotStarted
		} else {
			b.applyState = applyFailed
		}
		b.applyErr = err
	} else {
		b.applyState = applySucceeded
		b.applyErr = nil
	}
	b.applyAttempt.err = err
	close(b.applyAttempt.done)
	b.applyMu.Unlock()
	return err
}

func (b *Bear) cachedApplyResult() (error, bool) {
	b.applyMu.Lock()
	defer b.applyMu.Unlock()
	switch b.applyState {
	case applySucceeded:
		return nil, true
	case applyFailed:
		return b.applyErr, true
	default:
		return nil, false
	}
}

func (b *Bear) applyAll(ctx context.Context) (resultErr error) {
	lifecycleStartFailed := false
	defer func() {
		if recovered := recover(); recovered != nil {
			if recoveredErr, ok := recovered.(error); ok {
				resultErr = fmt.Errorf("ApplyAll failed while building application: %w", recoveredErr)
			} else {
				resultErr = fmt.Errorf("ApplyAll failed while building application: %v", recovered)
			}
		}
		if resultErr != nil && !lifecycleStartFailed {
			rollbackErr := runShutdownPhase(shutdownTimeout(b.runtime.Config), b.runtime.Lifecycle.Stop)
			if rollbackErr != nil {
				resultErr = errors.Join(resultErr, fmt.Errorf("ApplyAll rollback failed: %w", rollbackErr))
			}
		}
	}()
	if b.frameworkStrict() {
		if !b.strictBuildComplete {
			if err := b.buildStrictApplication(ctx); err != nil {
				return err
			}
		}
		b.runtime.Logger.Info("Executing component initializers...")
		if err := b.runtime.Lifecycle.Start(ctx); err != nil {
			lifecycleStartFailed = true
			return err
		}
		return nil
	}

	// Compatibility mode preserves the historical inject, Init, then Build order.
	for _, bean := range b.runtime.Container.orderedBeans() {
		v := reflect.ValueOf(bean)
		if v.Kind() == reflect.Ptr && v.Elem().Kind() == reflect.Struct {
			b.runtime.Container.Apply(bean)
		}
	}

	b.runtime.Logger.Info("Executing component initializers...")
	if err := b.runtime.Lifecycle.Start(ctx); err != nil {
		lifecycleStartFailed = true
		return err
	}

	b.runtime.Logger.Info("Building routes...")
	for _, mod := range b.modules {
		b.g = &b.Engine.RouterGroup
		mod.Build(b)
	}
	for _, m := range b.mounts {
		for _, class := range m.Classes {
			group := b.Engine.Group(m.Group)
			b.buildController(group, m.Group, class)
		}
	}

	return nil
}

const strictBuildRoundLimit = 32

func (b *Bear) buildStrictApplication(ctx context.Context) error {
	b.beginStrictInjectionSession()
	defer b.endStrictInjectionSession()

	b.runtime.Logger.Info("Building routes...")
	for round := 1; round <= strictBuildRoundLimit; round++ {
		if err := ctx.Err(); err != nil {
			return err
		}

		modules, moduleStart, moduleEnd, pluginModules := b.strictModuleBuildBatch()
		for offset, mod := range modules {
			if err := ctx.Err(); err != nil {
				return err
			}
			moduleIndex := moduleStart + offset
			if _, alreadyBuilt := pluginModules[moduleIndex]; alreadyBuilt {
				continue
			}
			if err := b.applyStrictObject(mod); err != nil {
				return fmt.Errorf("inject module %T: %w", mod, err)
			}
			if err := b.injectStrictContainerBeans(); err != nil {
				return fmt.Errorf("inject module beans before building %T: %w", mod, err)
			}
			b.g = &b.Engine.RouterGroup
			if err := b.buildModuleStrict(mod); err != nil {
				return err
			}
		}
		b.markStrictModulesBuilt(moduleEnd)
		if err := b.injectStrictContainerBeans(); err != nil {
			return fmt.Errorf("inject beans before building controllers: %w", err)
		}

		mounts, mountEnd := b.strictMountBuildBatch()
		for _, mount := range mounts {
			for _, class := range mount.Classes {
				if err := ctx.Err(); err != nil {
					return err
				}
				group := b.Engine.Group(mount.Group)
				if err := b.buildControllerE(group, mount.Group, class); err != nil {
					return err
				}
			}
		}
		b.markStrictMountsBuilt(mountEnd)

		stable, version, beanCount := b.strictBuildSnapshot()
		if !stable {
			continue
		}
		if err := b.injectStrictBeans(); err != nil {
			return err
		}
		if err := b.validateStrictWebSocketRoutes(); err != nil {
			return err
		}
		if b.sealStableStrictBuild(version, beanCount) {
			return nil
		}
	}
	return ErrBuildRegistrationLoop
}

func (b *Bear) validateStrictWebSocketRoutes() error {
	if b == nil || b.webSocketRoutes.Load() == 0 {
		return nil
	}
	config := b.runtime.Config
	if config == nil || config.WS == nil {
		return errors.New("websocket.allowed_origins must contain an explicit origin when strict WebSocket routes are registered")
	}
	for _, origin := range config.WS.GetAllowedOrigins() {
		origin = strings.TrimSpace(origin)
		if origin != "" && origin != "*" {
			return nil
		}
	}
	return errors.New("websocket.allowed_origins must contain an explicit origin when strict WebSocket routes are registered")
}

func (b *Bear) injectStrictBeans() error {
	if err := b.injectStrictContainerBeans(); err != nil {
		return err
	}
	for _, target := range b.strictInjectionTargetSnapshot() {
		if err := b.applyStrictObject(target); err != nil {
			return err
		}
	}
	return b.runtime.Container.strictConflictError()
}

func (b *Bear) injectStrictContainerBeans() error {
	if err := b.runtime.Container.strictConflictError(); err != nil {
		return err
	}
	for _, bean := range b.runtime.Container.orderedBeans() {
		if err := b.applyStrictObject(bean); err != nil {
			return err
		}
	}
	return b.runtime.Container.strictConflictError()
}

func (b *Bear) strictInjectionTargetSnapshot() []any {
	b.eRegistrationMu.Lock()
	targets := append([]any(nil), b.strictInjectionTargets...)
	b.eRegistrationMu.Unlock()
	return targets
}

func (b *Bear) applyStrictObject(value any) (resultErr error) {
	if value == nil || isNilBean(value) {
		return nil
	}
	v := reflect.ValueOf(value)
	if v.Kind() != reflect.Ptr || v.IsNil() || v.Elem().Kind() != reflect.Struct {
		return nil
	}
	b.strictInjectionMu.Lock()
	if b.strictInjectionSession {
		if _, applied := b.strictInjectionApplied[value]; applied {
			b.strictInjectionMu.Unlock()
			return nil
		}
	}
	if b.strictInjectionAttempts == nil {
		b.strictInjectionAttempts = make(map[any]*strictInjectionAttempt)
	}
	if attempt := b.strictInjectionAttempts[value]; attempt != nil {
		b.strictInjectionMu.Unlock()
		<-attempt.done
		return attempt.err
	}
	attempt := &strictInjectionAttempt{done: make(chan struct{})}
	b.strictInjectionAttempts[value] = attempt
	b.strictInjectionMu.Unlock()

	func() {
		defer func() {
			if recover() != nil {
				resultErr = fmt.Errorf("strict dependency injection panic for %T", value)
			}
		}()
		resultErr = b.runtime.Container.ApplyE(value)
	}()
	b.strictInjectionMu.Lock()
	attempt.err = resultErr
	if resultErr == nil && b.strictInjectionSession {
		b.strictInjectionApplied[value] = struct{}{}
	}
	delete(b.strictInjectionAttempts, value)
	close(attempt.done)
	b.strictInjectionMu.Unlock()
	return resultErr
}

func (b *Bear) beginStrictInjectionSession() {
	b.strictInjectionMu.Lock()
	b.strictInjectionSession = true
	b.strictInjectionApplied = make(map[any]struct{})
	b.strictInjectionMu.Unlock()
}

func (b *Bear) endStrictInjectionSession() {
	b.strictInjectionMu.Lock()
	b.strictInjectionSession = false
	b.strictInjectionApplied = nil
	b.strictInjectionMu.Unlock()
}

func (b *Bear) strictModuleBuildBatch() ([]Module, int, int, map[int]struct{}) {
	b.eRegistrationMu.Lock()
	defer b.eRegistrationMu.Unlock()
	start := b.strictBuiltModules
	end := len(b.modules)
	modules := append([]Module(nil), b.modules[start:end]...)
	pluginModules := make(map[int]struct{}, len(b.strictPluginModules))
	for index := range b.strictPluginModules {
		pluginModules[index] = struct{}{}
	}
	return modules, start, end, pluginModules
}

func (b *Bear) markStrictModulesBuilt(end int) {
	b.eRegistrationMu.Lock()
	if end > b.strictBuiltModules {
		b.strictBuiltModules = end
	}
	b.eRegistrationMu.Unlock()
}

func (b *Bear) strictMountBuildBatch() ([]MountMetadata, int) {
	b.eRegistrationMu.Lock()
	defer b.eRegistrationMu.Unlock()
	start := b.strictBuiltMounts
	end := len(b.mounts)
	mounts := make([]MountMetadata, 0, end-start)
	for _, mount := range b.mounts[start:end] {
		mounts = append(mounts, MountMetadata{
			Group:   mount.Group,
			Classes: append([]IClass(nil), mount.Classes...),
		})
	}
	return mounts, end
}

func (b *Bear) markStrictMountsBuilt(end int) {
	b.eRegistrationMu.Lock()
	if end > b.strictBuiltMounts {
		b.strictBuiltMounts = end
	}
	b.eRegistrationMu.Unlock()
}

func (b *Bear) strictBuildSnapshot() (bool, uint64, int) {
	b.eRegistrationMu.Lock()
	defer b.eRegistrationMu.Unlock()
	stable := b.strictBuiltModules == len(b.modules) && b.strictBuiltMounts == len(b.mounts)
	return stable, b.strictRegistrationVersion, len(b.runtime.Container.orderedBeans())
}

func (b *Bear) sealStableStrictBuild(version uint64, beanCount int) bool {
	b.eRegistrationMu.Lock()
	defer b.eRegistrationMu.Unlock()
	if b.strictBuiltModules != len(b.modules) || b.strictBuiltMounts != len(b.mounts) {
		return false
	}
	if b.strictRegistrationVersion != version || len(b.runtime.Container.orderedBeans()) != beanCount {
		return false
	}
	b.runtime.Lifecycle.sealRegistration()
	b.strictBuildComplete = true
	return true
}

func (b *Bear) buildModuleStrict(mod Module) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = strictBuildPanicError("module", mod, recovered)
		}
	}()
	if builder, ok := mod.(ModuleBuilderE); ok {
		if err := builder.BuildE(b); err != nil {
			return fmt.Errorf("module BuildE failed [%T]: %w", mod, err)
		}
		return nil
	}
	mod.Build(b)
	return nil
}

func (b *Bear) markStrictPluginModuleBuilt(mod Module) {
	b.eRegistrationMu.Lock()
	defer b.eRegistrationMu.Unlock()
	if b.strictPluginModules == nil {
		b.strictPluginModules = make(map[int]struct{})
	}
	for index := len(b.modules) - 1; index >= 0; index-- {
		if sameBeanInstance(b.modules[index], mod) {
			b.strictPluginModules[index] = struct{}{}
			return
		}
	}
}

func strictBuildPanicError(kind string, target any, recovered any) error {
	if recoveredErr, ok := recovered.(error); ok {
		return fmt.Errorf("%s build panic [%T]: %w", kind, target, recoveredErr)
	}
	return fmt.Errorf("%s build panic [%T]: %v", kind, target, recovered)
}

func (b *Bear) beginPluginRegistration() error {
	if b == nil || b.runtime == nil || b.runtime.Lifecycle.registrationClosed() {
		return ErrPluginHotReloadUnsupported
	}
	if b.pluginBarrier == nil {
		return ErrPluginHotReloadUnsupported
	}
	return b.pluginBarrier.begin()
}

func (b *Bear) endPluginRegistration() {
	if b == nil || b.pluginBarrier == nil {
		return
	}
	b.pluginBarrier.end()
}

func (b *Bear) closePluginRegistration(ctx context.Context) error {
	if b == nil || b.pluginBarrier == nil {
		return ErrPluginHotReloadUnsupported
	}
	return b.pluginBarrier.close(ctx)
}

// Group 创建路由组 (自动感知当前的挂载点)，支持 IClass 接口的 Handler 自动构建路由
func (b *Bear) Group(relativePath string, classes ...IClass) *gin.RouterGroup {
	group, err := b.GroupE(relativePath, classes...)
	if err != nil {
		panic(err)
	}
	return group
}

// GroupE creates a route group while strict registration is open.
func (b *Bear) GroupE(relativePath string, classes ...IClass) (*gin.RouterGroup, error) {
	unlock, err := b.beginGinRegistration()
	if err != nil {
		return nil, err
	}
	parent := b.activeGroup()
	group := parent.Group(relativePath)
	b.strictRegistrationVersion++
	unlock()

	// 自动调用 IClass 的 Build 方法构建路由
	for _, class := range classes {
		classGroup := parent.Group(relativePath)
		if err := b.buildControllerE(classGroup, classGroup.BasePath(), class); err != nil {
			return nil, err
		}
	}

	return group, nil
}

func (b *Bear) buildController(group *gin.RouterGroup, groupName string, class IClass) {
	if err := b.buildControllerE(group, groupName, class); err != nil {
		panic(err)
	}
}

func (b *Bear) buildControllerE(group *gin.RouterGroup, groupName string, class IClass) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			if !b.frameworkStrict() {
				panic(recovered)
			}
			err = strictBuildPanicError("controller", class, recovered)
		}
	}()
	if b.frameworkStrict() {
		if err := b.applyStrictObject(class); err != nil {
			return fmt.Errorf("inject controller %T: %w", class, err)
		}
	}
	parent := b.registration
	fairings := make([]Fairing, 0)
	if parent != nil {
		fairings = append(fairings, parent.fairings...)
	}
	var ownFairings []Fairing
	if inter, ok := class.(IInterceptors); ok {
		ownFairings = append([]Fairing(nil), inter.Interceptors()...)
		for _, fairing := range ownFairings {
			if b.frameworkStrict() {
				if err := b.applyStrictObject(fairing); err != nil {
					return fmt.Errorf("inject controller fairing %T: %w", fairing, err)
				}
			} else {
				b.runtime.Container.Apply(fairing)
			}
		}
		fairings = append(fairings, ownFairings...)
	}
	controller, _ := class.(IOpenAPI)
	registration := &routeRegistrationContext{
		parent:     parent,
		group:      group,
		groupName:  groupName,
		controller: controller,
		fairings:   fairings,
	}
	b.registration = registration
	defer func() {
		b.registration = registration.parent
	}()

	if b.frameworkStrict() {
		group.Use(func(ctx *gin.Context) {
			state := strictFairingStateFor(ctx)
			if err := b.runStrictGlobalFairings(ctx, state); err != nil {
				WriteError(ctx, err)
				return
			}
			if requestFairingTerminal(ctx) {
				ctx.Abort()
				return
			}
			if err := runEnteredRequestFairings(ctx, state, ownFairings); err != nil {
				WriteError(ctx, err)
				return
			}
			if requestFairingTerminal(ctx) {
				ctx.Abort()
				return
			}
			ctx.Next()
		})
	} else {
		for _, fairing := range ownFairings {
			current := fairing
			group.Use(func(ctx *gin.Context) {
				if requestFairingTerminal(ctx) {
					ctx.Abort()
					return
				}
				if err := current.OnRequest(ctx); err != nil {
					WriteError(ctx, err)
					return
				}
				if requestFairingTerminal(ctx) {
					ctx.Abort()
					return
				}
				ctx.Next()
			})
		}
	}
	if b.frameworkStrict() {
		if builder, ok := class.(ClassBuilderE); ok {
			if err := builder.BuildE(b); err != nil {
				return fmt.Errorf("controller BuildE failed [%T]: %w", class, err)
			}
			return nil
		}
	}
	class.Build(b)
	return nil
}

// POST 注册 POST 路由 (自动感知当前的挂载点)
func (b *Bear) POST(relativePath string, handlers ...gin.HandlerFunc) gin.IRoutes {
	routes, err := b.POSTE(relativePath, handlers...)
	if err != nil {
		panic(err)
	}
	return routes
}

func (b *Bear) POSTE(relativePath string, handlers ...gin.HandlerFunc) (gin.IRoutes, error) {
	return b.registerGinRoutesE(func() gin.IRoutes { return b.activeGroup().POST(relativePath, handlers...) })
}

// GET 注册 GET 路由 (自动感知当前的挂载点)
func (b *Bear) GET(relativePath string, handlers ...gin.HandlerFunc) gin.IRoutes {
	routes, err := b.GETE(relativePath, handlers...)
	if err != nil {
		panic(err)
	}
	return routes
}

func (b *Bear) GETE(relativePath string, handlers ...gin.HandlerFunc) (gin.IRoutes, error) {
	return b.registerGinRoutesE(func() gin.IRoutes { return b.activeGroup().GET(relativePath, handlers...) })
}

// PUT 注册 PUT 路由 (自动感知当前的挂载点)
func (b *Bear) PUT(relativePath string, handlers ...gin.HandlerFunc) gin.IRoutes {
	routes, err := b.PUTE(relativePath, handlers...)
	if err != nil {
		panic(err)
	}
	return routes
}

func (b *Bear) PUTE(relativePath string, handlers ...gin.HandlerFunc) (gin.IRoutes, error) {
	return b.registerGinRoutesE(func() gin.IRoutes { return b.activeGroup().PUT(relativePath, handlers...) })
}

// DELETE 注册 DELETE 路由 (自动感知当前的挂载点)
func (b *Bear) DELETE(relativePath string, handlers ...gin.HandlerFunc) gin.IRoutes {
	routes, err := b.DELETEE(relativePath, handlers...)
	if err != nil {
		panic(err)
	}
	return routes
}

func (b *Bear) DELETEE(relativePath string, handlers ...gin.HandlerFunc) (gin.IRoutes, error) {
	return b.registerGinRoutesE(func() gin.IRoutes { return b.activeGroup().DELETE(relativePath, handlers...) })
}

// PATCH 注册 PATCH 路由 (自动感知当前的挂载点)
func (b *Bear) PATCH(relativePath string, handlers ...gin.HandlerFunc) gin.IRoutes {
	routes, err := b.PATCHE(relativePath, handlers...)
	if err != nil {
		panic(err)
	}
	return routes
}

func (b *Bear) PATCHE(relativePath string, handlers ...gin.HandlerFunc) (gin.IRoutes, error) {
	return b.registerGinRoutesE(func() gin.IRoutes { return b.activeGroup().PATCH(relativePath, handlers...) })
}

// OPTIONS 注册 OPTIONS 路由 (自动感知当前的挂载点)
func (b *Bear) OPTIONS(relativePath string, handlers ...gin.HandlerFunc) gin.IRoutes {
	routes, err := b.OPTIONSE(relativePath, handlers...)
	if err != nil {
		panic(err)
	}
	return routes
}

func (b *Bear) OPTIONSE(relativePath string, handlers ...gin.HandlerFunc) (gin.IRoutes, error) {
	return b.registerGinRoutesE(func() gin.IRoutes { return b.activeGroup().OPTIONS(relativePath, handlers...) })
}

// HEAD 注册 HEAD 路由 (自动感知当前的挂载点)
func (b *Bear) HEAD(relativePath string, handlers ...gin.HandlerFunc) gin.IRoutes {
	routes, err := b.HEADE(relativePath, handlers...)
	if err != nil {
		panic(err)
	}
	return routes
}

func (b *Bear) HEADE(relativePath string, handlers ...gin.HandlerFunc) (gin.IRoutes, error) {
	return b.registerGinRoutesE(func() gin.IRoutes { return b.activeGroup().HEAD(relativePath, handlers...) })
}

// Any 注册 Any 路由 (自动感知当前的挂载点)
func (b *Bear) Any(relativePath string, handlers ...gin.HandlerFunc) gin.IRoutes {
	routes, err := b.AnyE(relativePath, handlers...)
	if err != nil {
		panic(err)
	}
	return routes
}

func (b *Bear) AnyE(relativePath string, handlers ...gin.HandlerFunc) (gin.IRoutes, error) {
	return b.registerGinRoutesE(func() gin.IRoutes { return b.activeGroup().Any(relativePath, handlers...) })
}

func (b *Bear) registerGinRoutesE(register func() gin.IRoutes) (gin.IRoutes, error) {
	unlock, err := b.beginGinRegistration()
	if err != nil {
		return nil, err
	}
	defer unlock()
	routes := register()
	b.strictRegistrationVersion++
	return routes, nil
}
