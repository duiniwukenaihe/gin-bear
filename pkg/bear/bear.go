package bear

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"reflect"
	"slices"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"go.opentelemetry.io/otel"
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

// Bear 是核心框架引擎
type Bear struct {
	*gin.Engine
	g                 *gin.RouterGroup
	exprData          map[string]interface{}
	currentGroup      string
	fairingHandler    *FairingHandler
	routeTree         *RouteTree // 路由树，用于存储路由级别的 Fairing
	onShutdown        []func()
	routeRegistry     []RouteMetadata
	grpcServices      []GRPCService
	mounts            []MountMetadata
	modules           []Module
	applied           atomic.Bool // 增加应用标记
	pluginDispatcher  *PluginDispatcher
	pluginManager     *PluginManager
	pluginMode        bool // 标记当前是否处于插件加载模式
	metricsRegistered atomic.Bool
	tracingRegistered atomic.Bool
}

// OnShutdown 注册服务关闭时的清理函数
func (b *Bear) OnShutdown(f ...func()) {
	b.onShutdown = append(b.onShutdown, f...)
}

func (b *Bear) Name() string {
	return "Bear"
}

// Ignite 初始化 Bear 引擎 (轻量级内核)
// 出山 - 象征小白熊破冰而出，准备开始工作
func Ignite(args ...any) *Bear {
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

	b := &Bear{
		Engine:           gin.New(),
		exprData:         map[string]interface{}{},
		fairingHandler:   NewFairingHandler(),
		routeTree:        NewRouteTree(),
		pluginDispatcher: NewPluginDispatcher(),
	}
	b.pluginManager = NewPluginManager(b)

	if config == nil {
		config = InitConfig()
	} else {
		config.PostProcess()
	}

	if config.DB != nil && config.DB.Enabled && config.DB.DSN == "" && config.DB.DBName == "" {
		panic("database configuration is required when database.enabled=true (dsn or dbname)")
	}
	if err := validateProductionSecurity(config); err != nil {
		panic(err.Error())
	}

	// 注册核心底座 Bean
	SetDefaultLogger(config)
	for _, warning := range config.compatibilityWarnings() {
		slog.Warn(warning)
	}
	GetInjector().Set(b)
	GetInjector().Set(config)
	configureGinRuntime(b, config)

	// 禁用 Gin 默认日志，由核心性能中间件接管结构化日志
	gin.DefaultWriter = io.Discard
	gin.DefaultErrorWriter = os.Stderr

	// 注入底座中间件
	b.Use(RequestIDMiddleware())
	b.Use(PerformanceMiddleware())
	b.Use(RecoveryMiddleware())
	b.Use(b.pluginDispatcher.Dispatch())
	for _, middleware := range ginMiddlewares {
		b.Use(middleware)
	}

	slog.Info("WhiteBear core awakened", "server", config.Server.Name)
	return b
}

// Deprecated: EnableIDGenerator is compatibility-only and has no effect.
func (b *Bear) EnableIDGenerator() error {
	slog.Warn("ID Generator is disabled in精简 mode")
	return nil
}

// Deprecated: EnableMQ is compatibility-only and has no effect.
func (b *Bear) EnableMQ(ctx context.Context) *Bear {
	slog.Warn("MQ is disabled in 精简 mode")
	return b
}

// EnableTracing 开启链路追踪
func (b *Bear) EnableTracing(ctx context.Context) *Bear {
	config := GetByType[*SysConfig]()
	if config == nil || config.Tracing == nil || !config.Tracing.Enabled {
		return b
	}
	if !b.tracingRegistered.CompareAndSwap(false, true) {
		return b
	}
	provider, err := newTracerProvider(ctx, config.Tracing)
	if err != nil {
		slog.Error("Tracing initialization failed", "error", err)
		return b
	}
	propagator := propagation.TraceContext{}
	otel.SetTracerProvider(provider)
	otel.SetTextMapPropagator(propagator)
	b.Use(TracingMiddleware(provider, propagator))
	b.OnShutdown(shutdownTracerProvider(provider))
	slog.Info("Tracing enabled", "exporter", config.Tracing.Exporter, "service", config.Tracing.ServiceName)
	return b
}

// EnableMetrics 开启指标监控
func (b *Bear) EnableMetrics() *Bear {
	config := GetByType[*SysConfig]()
	if config != nil && config.Metrics != nil && !config.Metrics.Enabled {
		return b
	}
	if !b.metricsRegistered.CompareAndSwap(false, true) {
		return b
	}
	path := "/metrics"
	if config != nil && config.Metrics != nil && config.Metrics.Path != "" {
		path = config.Metrics.Path
	}
	b.GET(path, func(ctx *gin.Context) {
		ctx.Header("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		ctx.String(http.StatusOK, httpMetrics.RenderPrometheus())
	})
	return b
}

// Launch 启动 Bear 引擎，支持优雅退出
// ctx 用于控制启动过程中的超时和取消操作
func (b *Bear) Launch(ctx context.Context) error {
	config := GetInjector().Get(reflect.TypeOf((*SysConfig)(nil))).(*SysConfig)

	// MQ 启动已禁用 (精简模式)

	srv := b.buildHTTPServer(config)

	// HTTP 服务器启动错误通道
	httpErrCh := make(chan error, 1)
	go func() {
		slog.Info("WhiteBear is emerging from ice", "addr", srv.Addr, "name", config.Server.Name)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			httpErrCh <- err
		}
	}()

	// 启动 gRPC 服务 (阶段 49)
	if config.GRPC != nil && config.GRPC.Enabled {
		grpcAddr := fmt.Sprintf(":%d", config.GRPC.Port)
		lis, err := net.Listen("tcp", grpcAddr)
		if err != nil {
			return fmt.Errorf("failed to listen for gRPC: %w", err)
		}

		grpcServer := grpc.NewServer()

		// 注册服务
		for _, s := range b.grpcServices {
			slog.Info("Registering gRPC service", "name", s.Name())
			s.Register(grpcServer)
		}

		go func() {
			slog.Info("WhiteBear gRPC is listening", "addr", grpcAddr)
			if err := grpcServer.Serve(lis); err != nil {
				slog.Error("Failed to serve gRPC", "error", err)
			}
		}()

		b.OnShutdown(func() {
			slog.Info("Shutting down WhiteBear gRPC...")
			grpcServer.GracefulStop()
		})
	}

	// 优雅退出处理
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	select {
	case err := <-httpErrCh:
		return fmt.Errorf("failed to launch bear: %w", err)
	case <-quit:
		slog.Info("Shutting down WhiteBear...")
	case <-ctx.Done():
		slog.Info("Context cancelled, shutting down...")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout(config))
	defer cancel()

	// 自动从 IoC 容器中查找并关闭一些标准组件 (示例)
	// 如果有 WorkerPool 等组件，可以在这里统一处理

	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("Bear shutdown forced", "error", err)
	}

	// 倒序执行清理钩子 (后注册的先退出，通常符合依赖关系)
	for i := len(b.onShutdown) - 1; i >= 0; i-- {
		b.onShutdown[i]()
	}

	// 自动化组件关闭：按照 LIFO (后进先出) 顺序执行销毁，确保依赖安全
	slog.Info("Automating component shutdown (LIFO)...")

	// 获取所有 Bean 并筛选 Shutdowner
	var shutdowners []Shutdowner
	for _, v := range GetInjector().GetBeanMapper() {
		if s, ok := v.Interface().(Shutdowner); ok {
			shutdowners = append(shutdowners, s)
		}
	}

	// 简单的后注册先退出策略 (实际场景中建议在 Register 时记录顺序)
	for i := len(shutdowners) - 1; i >= 0; i-- {
		s := shutdowners[i]
		name := "Unknown"
		if b, ok := s.(Bean); ok {
			name = b.Name()
		}
		slog.Info("Shutting down component", "name", name)
		if err := s.Shutdown(); err != nil {
			slog.Error("Component shutdown failed", "name", name, "error", err)
		}
	}

	slog.Info("WhiteBear returning to ice")
	return nil
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

func configureGinRuntime(b *Bear, config *SysConfig) {
	mode := configuredGinMode(config)
	gin.SetMode(mode)
	if config != nil && config.Server != nil && len(config.Server.TrustedProxies) > 0 {
		if err := b.Engine.SetTrustedProxies(config.Server.TrustedProxies); err != nil {
			panic(fmt.Sprintf("invalid trusted proxies: %v", err))
		}
	}
}

func configuredGinMode(config *SysConfig) string {
	if config != nil && config.Server != nil && config.Server.Mode != "" {
		return config.Server.Mode
	}
	if mode := os.Getenv("GIN_MODE"); mode != "" {
		return mode
	}
	env := os.Getenv("BEAR_ENV")
	if env == "prod" || env == "production" {
		return gin.ReleaseMode
	}
	return gin.DebugMode
}

func isProductionMode(config *SysConfig) bool {
	mode := configuredGinMode(config)
	if mode == gin.ReleaseMode {
		return true
	}
	env := os.Getenv("BEAR_ENV")
	return env == "prod" || env == "production"
}

func validateProductionSecurity(config *SysConfig) error {
	if !isProductionMode(config) {
		return nil
	}
	if config == nil {
		return nil
	}
	if config.Auth != nil {
		secret := config.Auth.JWTSecret
		if secret == "" || secret == "bear-secret" || secret == "your-secret-key" || len(secret) < 32 {
			return fmt.Errorf("weak jwt secret is not allowed in production")
		}
	}
	if config.WS != nil && !config.WS.CheckOrigin && len(config.WS.AllowedOrigins) == 0 {
		return fmt.Errorf("websocket origin check cannot be disabled in production without allowed origins")
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
	b.Mount("", &HealthController{})
	config := GetByType[*SysConfig]()
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
		GetInjector().Set(bean)
	}
	return b
}

// Attach 注册全局 Fairing
func (b *Bear) Attach(f ...Fairing) *Bear {
	b.fairingHandler.AddFairing(f...)
	for _, f1 := range f {
		GetInjector().Set(f1)
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
		slog.Info("Loading module", "name", mod.Name())
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
	GetInjector().Apply(handler)

	b.activeGroup().GET(relativePath, func(ctx *gin.Context) {
		// 2. 触发 Fairing OnRequest (支持鉴权、限流等)
		if err := b.fairingHandler.OnRequest(ctx); err != nil {
			return
		}

		// 3. 获取 WS 配置并初始化升级程序
		config := GetByType[*SysConfig]()
		upgrader := websocket.Upgrader{
			HandshakeTimeout: time.Duration(config.WS.HandshakeTimeout) * time.Millisecond,
			ReadBufferSize:   config.WS.ReadBufferSize,
			WriteBufferSize:  config.WS.WriteBufferSize,
			CheckOrigin: func(r *http.Request) bool {
				return websocketOriginAllowed(config, r)
			},
		}

		// 4. 升级协议
		conn, err := upgrader.Upgrade(ctx.Writer, ctx.Request, nil)
		if err != nil {
			slog.ErrorContext(ctx.Request.Context(), "WebSocket upgrade failed", "error", err)
			return
		}
		defer conn.Close()

		// 5. 调用 OnConnect
		if err := handler.OnConnect(ctx, conn); err != nil {
			slog.ErrorContext(ctx.Request.Context(), "WebSocket OnConnect failed", "error", err)
			return
		}

		// 6. 消息处理循环
		for {
			messageType, p, err := conn.ReadMessage()
			if err != nil {
				slog.InfoContext(ctx.Request.Context(), "WebSocket connection closed", "error", err)
				handler.OnClose(ctx, conn)
				break
			}
			if err := handler.OnMessage(ctx, conn, messageType, p); err != nil {
				slog.ErrorContext(ctx.Request.Context(), "WebSocket OnMessage error", "error", err)
			}
		}
	})
	return b
}

// Handle 注册路由，支持 Responder 转换
func (b *Bear) Handle(httpMethod, relativePath string, handler interface{}) *Bear {
	if b.pluginMode {
		// 如果处于插件模式，注册到动态分发器
		if h, ok := handler.(func(*gin.Context)); ok {
			b.pluginDispatcher.Register(httpMethod, relativePath, h)
		} else if h := Convert(handler); h != nil {
			b.pluginDispatcher.Register(httpMethod, relativePath, h)
		}
		return b
	}

	if h := Convert(handler); h != nil {
		// 包装 handler 以支持全局 Fairing
		wrappedHandler := b.wrapWithFairing(h)
		b.activeGroup().Handle(httpMethod, relativePath, wrappedHandler)

		// 记录路由元数据
		b.routeRegistry = append(b.routeRegistry, RouteMetadata{
			Method:      httpMethod,
			Path:        relativePath,
			GroupName:   b.currentGroup,
			HandlerType: reflect.TypeOf(handler),
			HandlerName: runtimeFuncName(handler),
		})
	}
	return b
}

// HandleWithFairing 注册路由并绑定路由级别的 Fairing
// 路由级别的 Fairing 会在全局 Fairing 之前执行（OnRequest）
// 在全局 Fairing 之后执行（OnResponse）
func (b *Bear) HandleWithFairing(httpMethod, relativePath string, handler interface{}, fairings ...Fairing) *Bear {
	// 1. 对 fairings 进行依赖注入
	for _, f := range fairings {
		GetInjector().Apply(f)
	}

	// 2. 注册到路由树
	b.routeTree.addRoute(httpMethod, relativePath, fairings)

	// 3. 使用包装器处理请求，集成路由级别的 Fairing
	wrappedHandler := b.wrapWithRouteFairing(handler, fairings)

	if b.pluginMode {
		// 插件模式下注册到动态分发器
		b.pluginDispatcher.Register(httpMethod, relativePath, wrappedHandler)
		return b
	}

	b.activeGroup().Handle(httpMethod, relativePath, wrappedHandler)

	// 4. 记录路由元数据
	b.routeRegistry = append(b.routeRegistry, RouteMetadata{
		Method:      httpMethod,
		Path:        relativePath,
		GroupName:   b.currentGroup,
		HandlerType: reflect.TypeOf(handler),
		HandlerName: runtimeFuncName(handler),
	})

	return b
}

func (b *Bear) activeGroup() *gin.RouterGroup {
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
	if len(config.WS.AllowedOrigins) > 0 {
		return origin == "" || slices.Contains(config.WS.AllowedOrigins, origin) || slices.Contains(config.WS.AllowedOrigins, "*")
	}
	if !config.WS.CheckOrigin {
		return true
	}
	return origin == "" || origin == "http://"+r.Host || origin == "https://"+r.Host
}

// wrapWithRouteFairing 包装 handler 以集成路由级别的 Fairing
func (b *Bear) wrapWithRouteFairing(handler interface{}, fairings []Fairing) gin.HandlerFunc {
	return func(ctx *gin.Context) {
		// 1. 执行路由级别 Fairing 的 OnRequest
		for _, f := range fairings {
			if err := f.OnRequest(ctx); err != nil {
				ctx.AbortWithStatusJSON(400, Response{
					Code:    400,
					Message: err.Error(),
				})
				return
			}
		}

		// 2. 执行原始 handler 并捕获结果
		var result interface{}
		if h := Convert(handler); h != nil {
			h(ctx)
			// 从上下文获取处理后的结果（如果有）
			if r, ok := ctx.Get("bear_handler_result"); ok {
				result = r
			}
		} else if hf, ok := handler.(gin.HandlerFunc); ok {
			hf(ctx)
			if r, ok := ctx.Get("bear_handler_result"); ok {
				result = r
			}
		}

		// 3. 如果有结果，执行路由级别 Fairing 的 OnResponse
		if result != nil {
			for _, f := range fairings {
				if res, err := f.OnResponse(result); err == nil {
					result = res
				}
			}
			// 将处理后的结果存回上下文，供后续全局 Fairing 使用
			ctx.Set("bear_route_fairing_result", result)
		}
	}
}

// wrapWithFairing 包装 handler 以集成全局 Fairing
func (b *Bear) wrapWithFairing(handler gin.HandlerFunc) gin.HandlerFunc {
	return func(ctx *gin.Context) {
		// 执行全局 Fairing 的 OnRequest
		if err := b.fairingHandler.OnRequest(ctx); err != nil {
			ctx.AbortWithStatusJSON(400, Response{
				Code:    400,
				Message: err.Error(),
			})
			return
		}
		// 执行原始 handler
		handler(ctx)
	}
}

func runtimeFuncName(i interface{}) string {
	return reflect.TypeOf(i).String()
}

// ApplyAll 应用依赖注入并执行初始化
// ctx 用于控制初始化过程中的超时和取消操作
func (b *Bear) ApplyAll(ctx context.Context) error {
	if !b.applied.CompareAndSwap(false, true) {
		slog.Warn("ApplyAll already called, skipping redundant initialization")
		return nil
	}
	// 1. 第一遍遍历：执行字段注入
	for _, v := range GetInjector().GetBeanMapper() {
		if v.Kind() == reflect.Ptr && v.Elem().Kind() == reflect.Struct {
			GetInjector().Apply(v.Interface())
		}
	}

	// 2. 第二遍遍历：执行 Init 初始化钩子
	slog.Info("Executing component initializers...")
	for _, v := range GetInjector().GetBeanMapper() {
		if initializer, ok := v.Interface().(Initializer); ok {
			slog.Info("Initializing component", "name", v.Interface().(Bean).Name())
			if err := initializer.Init(ctx); err != nil {
				return fmt.Errorf("component initialization failed [%s]: %w", v.Interface().(Bean).Name(), err)
			}
		}
	}

	// 3. 构建路由 (确保在注入之后)
	slog.Info("Building routes...")

	// 3.1 先处理模块 (模块的 Build() 可能会调用 b.Mount() 添加控制器)
	for _, mod := range b.modules {
		// 模块构建前重置当前 group 为根路径
		b.g = &b.Engine.RouterGroup
		mod.Build(b)
	}

	// 3.2 后处理 mounts (包括模块中添加的控制器)
	for _, m := range b.mounts {
		b.g = b.Engine.Group(m.Group)
		for _, class := range m.Classes {
			b.currentGroup = m.Group
			// 检查是否有控制器级别的拦截器
			if inter, ok := class.(IInterceptors); ok {
				for _, f := range inter.Interceptors() {
					b.g.Use(func(ctx *gin.Context) {
						if err := f.OnRequest(ctx); err != nil {
							ctx.AbortWithStatusJSON(400, gin.H{"error": err.Error()})
							return
						}
						ctx.Next()
					})
				}
			}
			class.Build(b)
		}
	}

	// 预热 Handler 缓存
	WarmupHandlers(b.routeRegistry)
	slog.Info("Handler cache warmed up", "count", len(b.routeRegistry))

	return nil
}

// Group 创建路由组 (自动感知当前的挂载点)，支持 IClass 接口的 Handler 自动构建路由
func (b *Bear) Group(relativePath string, classes ...IClass) *gin.RouterGroup {
	var group *gin.RouterGroup
	if b.g != nil {
		group = b.g.Group(relativePath)
	} else {
		group = b.Engine.Group(relativePath)
	}

	// 自动调用 IClass 的 Build 方法构建路由
	for _, class := range classes {
		class.Build(b)
	}

	return group
}

// POST 注册 POST 路由 (自动感知当前的挂载点)
func (b *Bear) POST(relativePath string, handlers ...gin.HandlerFunc) gin.IRoutes {
	if b.g != nil {
		return b.g.POST(relativePath, handlers...)
	}
	return b.Engine.POST(relativePath, handlers...)
}

// GET 注册 GET 路由 (自动感知当前的挂载点)
func (b *Bear) GET(relativePath string, handlers ...gin.HandlerFunc) gin.IRoutes {
	if b.g != nil {
		return b.g.GET(relativePath, handlers...)
	}
	return b.Engine.GET(relativePath, handlers...)
}

// PUT 注册 PUT 路由 (自动感知当前的挂载点)
func (b *Bear) PUT(relativePath string, handlers ...gin.HandlerFunc) gin.IRoutes {
	if b.g != nil {
		return b.g.PUT(relativePath, handlers...)
	}
	return b.Engine.PUT(relativePath, handlers...)
}

// DELETE 注册 DELETE 路由 (自动感知当前的挂载点)
func (b *Bear) DELETE(relativePath string, handlers ...gin.HandlerFunc) gin.IRoutes {
	if b.g != nil {
		return b.g.DELETE(relativePath, handlers...)
	}
	return b.Engine.DELETE(relativePath, handlers...)
}

// PATCH 注册 PATCH 路由 (自动感知当前的挂载点)
func (b *Bear) PATCH(relativePath string, handlers ...gin.HandlerFunc) gin.IRoutes {
	if b.g != nil {
		return b.g.PATCH(relativePath, handlers...)
	}
	return b.Engine.PATCH(relativePath, handlers...)
}

// OPTIONS 注册 OPTIONS 路由 (自动感知当前的挂载点)
func (b *Bear) OPTIONS(relativePath string, handlers ...gin.HandlerFunc) gin.IRoutes {
	if b.g != nil {
		return b.g.OPTIONS(relativePath, handlers...)
	}
	return b.Engine.OPTIONS(relativePath, handlers...)
}

// HEAD 注册 HEAD 路由 (自动感知当前的挂载点)
func (b *Bear) HEAD(relativePath string, handlers ...gin.HandlerFunc) gin.IRoutes {
	if b.g != nil {
		return b.g.HEAD(relativePath, handlers...)
	}
	return b.Engine.HEAD(relativePath, handlers...)
}

// Any 注册 Any 路由 (自动感知当前的挂载点)
func (b *Bear) Any(relativePath string, handlers ...gin.HandlerFunc) gin.IRoutes {
	if b.g != nil {
		return b.g.Any(relativePath, handlers...)
	}
	return b.Engine.Any(relativePath, handlers...)
}
