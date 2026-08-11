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

// IClass 定义了控制器接口
type IClass interface {
	Bean
	Build(bear *Bear)
}

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

// Bear 是核心框架引擎
type Bear struct {
	*gin.Engine
	g                 *gin.RouterGroup
	exprData          map[string]interface{}
	fairingHandler    *FairingHandler
	routeTree         *RouteTree // 路由树，用于存储路由级别的 Fairing
	routeRegistry     []RouteMetadata
	registration      *routeRegistrationContext
	grpcServices      []GRPCService
	mounts            []MountMetadata
	modules           []Module
	runtime           *Runtime
	applyMu           sync.Mutex
	applyState        applyState
	applyErr          error
	applyDone         chan struct{}
	pluginBarrier     *pluginRegistrationBarrier
	pluginDispatcher  *PluginDispatcher
	pluginManager     *PluginManager
	pluginMode        bool // 标记当前是否处于插件加载模式
	metricsRegistered atomic.Bool
	tracingRegistered atomic.Bool
}

type applyState uint8

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
		return nil, fmt.Errorf("Invalid configuration: %w", err)
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
	publishDefaultRuntime(runtime)
	for _, warning := range config.compatibilityWarnings() {
		runtime.Logger.Warn(warning)
	}
	if err := configureGinRuntime(b, config); err != nil {
		return nil, err
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
	config := b.runtime.Config
	if config == nil || config.Tracing == nil || !config.Tracing.Enabled {
		return b
	}
	if b.runtime.Lifecycle.registrationClosed() {
		b.runtime.Logger.Error("Tracing registration rejected", "error_code", "BEAR_TRACING_REGISTRATION_CLOSED")
		return b
	}
	if !b.tracingRegistered.CompareAndSwap(false, true) {
		return b
	}
	provider, err := newTracerProvider(ctx, config.Tracing)
	if err != nil {
		b.tracingRegistered.Store(false)
		b.runtime.Logger.Error("Tracing initialization failed", "error_code", "BEAR_TRACING_INIT")
		return b
	}
	if err := b.TryOnShutdown(shutdownTracerProvider(provider)); err != nil {
		shutdownTracerProvider(provider)()
		b.tracingRegistered.Store(false)
		b.runtime.Logger.Error("Tracing registration rejected", "error_code", "BEAR_TRACING_REGISTRATION_CLOSED")
		return b
	}
	propagator := propagation.TraceContext{}
	b.runtime.TracerProvider = provider
	b.runtime.TextMapPropagator = propagator
	b.Use(TracingMiddleware(provider, propagator))
	b.runtime.Logger.Info("Tracing enabled", "exporter", config.Tracing.Exporter, "service", config.Tracing.ServiceName)
	return b
}

// EnableMetrics 开启指标监控
func (b *Bear) EnableMetrics() *Bear {
	config := b.runtime.Config
	if config != nil && config.Metrics != nil && !config.Metrics.Enabled {
		return b
	}
	if !b.metricsRegistered.CompareAndSwap(false, true) {
		return b
	}
	if b.runtime.Metrics == nil {
		return b
	}
	path := "/metrics"
	if config != nil && config.Metrics != nil && config.Metrics.Path != "" {
		path = config.Metrics.Path
	}
	b.GET(path, gin.WrapH(b.runtime.Metrics.Handler()))
	return b
}

// Launch 启动 Bear 引擎，支持优雅退出
// ctx 用于控制启动过程中的超时和取消操作
func (b *Bear) Launch(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := b.launchApplyError(); err != nil {
		return err
	}
	if err := b.closePluginRegistration(ctx); err != nil {
		return err
	}
	b.runtime.Lifecycle.sealRegistration()
	if err := b.runtime.Lifecycle.Start(ctx); err != nil {
		return err
	}
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
	signalCtx, stopSignals := signalNotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stopSignals()
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
	case <-signalCtx.Done():
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

func configureGinRuntime(b *Bear, config *SysConfig) error {
	if config != nil && config.Server != nil {
		if err := b.Engine.SetTrustedProxies(config.Server.TrustedProxies); err != nil {
			return fmt.Errorf("invalid trusted proxies: %w", err)
		}
	}
	return nil
}

func newGinEngine(config *SysConfig) (engine *gin.Engine, err error) {
	ginRuntimeMu.Lock()
	defer ginRuntimeMu.Unlock()
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("construct gin engine: %v", recovered)
		}
	}()
	gin.SetMode(configuredGinMode(config))
	gin.DefaultWriter = io.Discard
	gin.DefaultErrorWriter = os.Stderr
	return gin.New(), nil
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
	b.mounts = append(b.mounts, MountMetadata{Group: group, Classes: classes})
	for _, class := range classes {
		b.Beans(class)
	}
	return b
}

// EnableHealth 启用健康检查与指标端点
func (b *Bear) EnableHealth() *Bear {
	b.Mount("", &HealthController{runtime: b.runtime})
	config := b.runtime.Config
	if config == nil || config.Metrics == nil || config.Metrics.Enabled {
		b.EnableMetrics()
	}
	return b
}

// EnableGzip 启用 Gzip 响应压缩 (阶段 84)
func (b *Bear) EnableGzip(minLength ...int) *Bear {
	limit := 1024
	if len(minLength) > 0 {
		limit = minLength[0]
	}
	b.Use(GzipMiddleware(limit))
	return b
}

// Beans 注册 Bean
func (b *Bear) Beans(beans ...Bean) *Bear {
	for _, bean := range beans {
		b.exprData[bean.Name()] = bean
		b.runtime.Container.Set(bean)
	}
	return b
}

// Attach 注册全局 Fairing
func (b *Bear) Attach(f ...Fairing) *Bear {
	b.fairingHandler.AddFairing(f...)
	for _, f1 := range f {
		b.runtime.Container.Set(f1)
	}
	return b
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
	for _, mod := range modules {
		b.runtime.Logger.Info("Loading module", "name", mod.Name())
		// 1. 注册模块中的 Beans
		b.Beans(mod.Beans()...)
		// 2. 暂存模块
		b.modules = append(b.modules, mod)
	}
	return b
}

// HandleWS 注册 WebSocket 路由
func (b *Bear) HandleWS(relativePath string, handler WebSocketHandler) *Bear {
	// 1. 执行依赖注入
	b.runtime.Container.Apply(handler)

	b.activeGroup().GET(relativePath, func(ctx *gin.Context) {
		// 2. 触发 Fairing OnRequest (支持鉴权、限流等)
		if err := b.fairingHandler.OnRequest(ctx); err != nil {
			WriteError(ctx, err)
			return
		}
		if b.runtime.hijackedShutdownStarted() {
			ctx.AbortWithStatus(http.StatusServiceUnavailable)
			return
		}

		// 3. 获取 WS 配置并初始化升级程序
		config := b.runtime.Config
		policy := webSocketPolicyForConfig(config)
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
	return b
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
	wrappedHandler, err := b.compilePipeline(handler, fairings)
	if err != nil {
		panic(err)
	}

	for _, f := range fairings {
		b.runtime.Container.Apply(f)
	}
	b.routeTree.addRoute(httpMethod, relativePath, fairings)
	b.registerCompiledHandler(httpMethod, relativePath, handler, wrappedHandler, fairings)
	return b
}

func (b *Bear) registerHandler(httpMethod, relativePath string, handler interface{}, routeFairings []Fairing) error {
	wrappedHandler, err := b.compilePipeline(handler, routeFairings)
	if err != nil {
		return err
	}
	b.registerCompiledHandler(httpMethod, relativePath, handler, wrappedHandler, routeFairings)
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
			if err := b.runRequestFairings(ctx, routeFairings); err != nil {
				WriteError(ctx, err)
				return
			}
			if ctx.IsAborted() || ctx.Writer.Written() {
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
		if err := b.runRequestFairings(ctx, routeFairings); err != nil {
			WriteError(ctx, err)
			return
		}
		if ctx.IsAborted() || ctx.Writer.Written() {
			return
		}

		result, err := compiled(ctx)
		if err != nil {
			WriteError(ctx, err)
			return
		}
		if ctx.IsAborted() || ctx.Writer.Written() {
			return
		}

		result, err = b.fairingHandler.onResponse(result)
		if err != nil {
			WriteError(ctx, err)
			return
		}
		for _, fairing := range routeFairings {
			result, err = fairing.OnResponse(result)
			if err != nil {
				WriteError(ctx, err)
				return
			}
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
	if config == nil || config.WS == nil {
		return true
	}
	origin := r.Header.Get("Origin")
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
		done := b.applyDone
		b.applyMu.Unlock()
		select {
		case <-done:
			b.applyMu.Lock()
			err := b.applyErr
			b.applyMu.Unlock()
			return err
		case <-ctx.Done():
			return ctx.Err()
		}
	default:
		b.applyState = applyRunning
		b.applyDone = make(chan struct{})
		b.applyMu.Unlock()
	}

	err := b.applyAll(ctx)
	b.applyMu.Lock()
	if err != nil {
		b.applyState = applyFailed
		b.applyErr = err
	} else {
		b.applyState = applySucceeded
	}
	close(b.applyDone)
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
			resultErr = fmt.Errorf("ApplyAll failed while building application: %v", recovered)
		}
		if resultErr != nil && !lifecycleStartFailed {
			rollbackErr := runShutdownPhase(shutdownTimeout(b.runtime.Config), b.runtime.Lifecycle.Stop)
			if rollbackErr != nil {
				resultErr = errors.Join(resultErr, fmt.Errorf("ApplyAll rollback failed: %w", rollbackErr))
			}
		}
	}()
	// 1. 第一遍遍历：执行字段注入
	for _, bean := range b.runtime.Container.orderedBeans() {
		v := reflect.ValueOf(bean)
		if v.Kind() == reflect.Ptr && v.Elem().Kind() == reflect.Struct {
			b.runtime.Container.Apply(bean)
		}
	}

	// 2. 第二遍遍历：执行 Init 初始化钩子
	b.runtime.Logger.Info("Executing component initializers...")
	if err := b.runtime.Lifecycle.Start(ctx); err != nil {
		lifecycleStartFailed = true
		return err
	}

	// 3. 构建路由 (确保在注入之后)
	b.runtime.Logger.Info("Building routes...")

	// 3.1 先处理模块 (模块的 Build() 可能会调用 b.Mount() 添加控制器)
	for _, mod := range b.modules {
		// 模块构建前重置当前 group 为根路径
		b.g = &b.Engine.RouterGroup
		mod.Build(b)
	}

	// 3.2 后处理 mounts (包括模块中添加的控制器)
	for _, m := range b.mounts {
		for _, class := range m.Classes {
			group := b.Engine.Group(m.Group)
			b.buildController(group, m.Group, class)
		}
	}

	return nil
}

func (b *Bear) launchApplyError() error {
	b.applyMu.Lock()
	defer b.applyMu.Unlock()
	switch b.applyState {
	case applyFailed:
		return fmt.Errorf("launch blocked after ApplyAll failure: %w", b.applyErr)
	case applyRunning:
		return errors.New("launch blocked while ApplyAll is running")
	default:
		return nil
	}
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
	parent := b.activeGroup()
	group := parent.Group(relativePath)

	// 自动调用 IClass 的 Build 方法构建路由
	for _, class := range classes {
		classGroup := parent.Group(relativePath)
		b.buildController(classGroup, classGroup.BasePath(), class)
	}

	return group
}

func (b *Bear) buildController(group *gin.RouterGroup, groupName string, class IClass) {
	parent := b.registration
	fairings := make([]Fairing, 0)
	if parent != nil {
		fairings = append(fairings, parent.fairings...)
	}
	var ownFairings []Fairing
	if inter, ok := class.(IInterceptors); ok {
		ownFairings = append([]Fairing(nil), inter.Interceptors()...)
		for _, fairing := range ownFairings {
			b.runtime.Container.Apply(fairing)
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

	for _, fairing := range ownFairings {
		current := fairing
		group.Use(func(ctx *gin.Context) {
			if err := current.OnRequest(ctx); err != nil {
				WriteError(ctx, err)
				return
			}
			ctx.Next()
		})
	}
	class.Build(b)
}

// POST 注册 POST 路由 (自动感知当前的挂载点)
func (b *Bear) POST(relativePath string, handlers ...gin.HandlerFunc) gin.IRoutes {
	return b.activeGroup().POST(relativePath, handlers...)
}

// GET 注册 GET 路由 (自动感知当前的挂载点)
func (b *Bear) GET(relativePath string, handlers ...gin.HandlerFunc) gin.IRoutes {
	return b.activeGroup().GET(relativePath, handlers...)
}

// PUT 注册 PUT 路由 (自动感知当前的挂载点)
func (b *Bear) PUT(relativePath string, handlers ...gin.HandlerFunc) gin.IRoutes {
	return b.activeGroup().PUT(relativePath, handlers...)
}

// DELETE 注册 DELETE 路由 (自动感知当前的挂载点)
func (b *Bear) DELETE(relativePath string, handlers ...gin.HandlerFunc) gin.IRoutes {
	return b.activeGroup().DELETE(relativePath, handlers...)
}

// PATCH 注册 PATCH 路由 (自动感知当前的挂载点)
func (b *Bear) PATCH(relativePath string, handlers ...gin.HandlerFunc) gin.IRoutes {
	return b.activeGroup().PATCH(relativePath, handlers...)
}

// OPTIONS 注册 OPTIONS 路由 (自动感知当前的挂载点)
func (b *Bear) OPTIONS(relativePath string, handlers ...gin.HandlerFunc) gin.IRoutes {
	return b.activeGroup().OPTIONS(relativePath, handlers...)
}

// HEAD 注册 HEAD 路由 (自动感知当前的挂载点)
func (b *Bear) HEAD(relativePath string, handlers ...gin.HandlerFunc) gin.IRoutes {
	return b.activeGroup().HEAD(relativePath, handlers...)
}

// Any 注册 Any 路由 (自动感知当前的挂载点)
func (b *Bear) Any(relativePath string, handlers ...gin.HandlerFunc) gin.IRoutes {
	return b.activeGroup().Any(relativePath, handlers...)
}
