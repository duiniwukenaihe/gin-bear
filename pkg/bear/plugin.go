package bear

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"plugin"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
)

// ErrPluginHotReloadUnsupported reports that a plugin cannot be loaded once
// application startup has begun. Replace running instances through a rolling
// deployment instead of mutating their lifecycle in place.
var ErrPluginHotReloadUnsupported = errors.New("plugin hot reload unsupported after application startup begins; use rolling replacement")

// ErrPluginRouteConflict reports dynamic routes that match the same request
// shape but use different parameter names.
var ErrPluginRouteConflict = errors.New("plugin route conflicts with an existing dynamic route")

// PluginDispatcher 负责动态路由分发
type PluginDispatcher struct {
	// handlers are grouped by HTTP method. Each method keeps registration order
	// so equally-specific parameter routes have deterministic precedence.
	handlers map[string]*pluginMethodRoutes
	mu       sync.RWMutex
	logger   *slog.Logger
}

type pluginMethodRoutes struct {
	routes map[string]pluginRoute
	shapes map[string]string
	order  []string
}

type pluginRoute struct {
	handler  gin.HandlerFunc
	segments []string
	dynamic  bool
}

func NewPluginDispatcher() *PluginDispatcher {
	return newPluginDispatcher(nil)
}

func newPluginDispatcher(logger *slog.Logger) *PluginDispatcher {
	return &PluginDispatcher{
		handlers: make(map[string]*pluginMethodRoutes),
		logger:   logger,
	}
}

func (p *PluginDispatcher) Register(method, path string, handler gin.HandlerFunc) {
	if err := p.RegisterE(method, path, handler); err != nil {
		logger := p.logger
		if logger == nil {
			logger = legacyLogger()
		}
		logger.Error("Dynamic route registration rejected", "method", method, "path", path, "error", err)
	}
}

// RegisterE registers a dynamic route or rejects ambiguous route shapes.
func (p *PluginDispatcher) RegisterE(method, path string, handler gin.HandlerFunc) error {
	if p == nil {
		return errors.New("plugin dispatcher is unavailable")
	}
	if handler == nil {
		return errors.New("plugin route handler must not be nil")
	}
	method = strings.ToUpper(strings.TrimSpace(method))
	path = normalizePluginPath(path)
	shape := pluginRouteShape(path)
	p.mu.Lock()
	defer p.mu.Unlock()
	routes, ok := p.handlers[method]
	if !ok {
		routes = &pluginMethodRoutes{
			routes: make(map[string]pluginRoute),
			shapes: make(map[string]string),
		}
		p.handlers[method] = routes
	}
	if registeredPath, exists := routes.shapes[shape]; exists && registeredPath != path {
		return fmt.Errorf("%w: %s %s conflicts with %s", ErrPluginRouteConflict, method, path, registeredPath)
	}
	route := newPluginRoute(path, handler)
	if _, exists := routes.routes[path]; !exists {
		routes.order = append(routes.order, path)
	}
	routes.routes[path] = route
	routes.shapes[shape] = path
	logger := p.logger
	if logger == nil {
		logger = legacyLogger()
	}
	logger.Info("Dynamic route registered", "method", method, "path", path)
	return nil
}

func (p *PluginDispatcher) Unregister(method, path string) {
	method = strings.ToUpper(strings.TrimSpace(method))
	path = normalizePluginPath(path)
	p.mu.Lock()
	defer p.mu.Unlock()
	if routes, ok := p.handlers[method]; ok {
		if _, exists := routes.routes[path]; exists {
			delete(routes.shapes, pluginRouteShape(path))
		}
		delete(routes.routes, path)
	}
}

func (p *PluginDispatcher) Dispatch() gin.HandlerFunc {
	return p.dispatch(false)
}

func (p *PluginDispatcher) dispatchFallback() gin.HandlerFunc {
	return p.dispatch(true)
}

func (p *PluginDispatcher) dispatch(fallbackOnly bool) gin.HandlerFunc {
	return func(ctx *gin.Context) {
		if fallbackOnly && ctx.FullPath() != "" {
			ctx.Next()
			return
		}
		p.mu.RLock()
		method := ctx.Request.Method
		handler, params := p.match(method, ctx.Request.URL.Path)
		p.mu.RUnlock()
		if handler != nil {
			ctx.Params = params
			handler(ctx)
			ctx.Abort()
			return
		}
		ctx.Next()
	}
}

func newPluginRoute(path string, handler gin.HandlerFunc) pluginRoute {
	segments := splitPluginPath(path)
	route := pluginRoute{handler: handler, segments: segments}
	for _, segment := range segments {
		if len(segment) > 1 && (segment[0] == ':' || segment[0] == '*') {
			route.dynamic = true
			break
		}
	}
	return route
}

func (p *PluginDispatcher) match(method, path string) (gin.HandlerFunc, gin.Params) {
	routes, ok := p.handlers[method]
	if !ok {
		return nil, nil
	}
	if route, ok := routes.routes[path]; ok && !route.dynamic {
		return route.handler, nil
	}
	pathSegments := splitPluginPath(path)
	bestSpecificity := []int(nil)
	var bestHandler gin.HandlerFunc
	var bestParams gin.Params
	for _, registeredPath := range routes.order {
		route, ok := routes.routes[registeredPath]
		if !ok || !route.dynamic {
			continue
		}
		if params, ok := route.match(pathSegments); ok {
			specificity := route.specificity()
			if bestHandler == nil || pluginRouteMoreSpecific(specificity, bestSpecificity) {
				bestSpecificity = specificity
				bestHandler = route.handler
				bestParams = params
			}
		}
	}
	return bestHandler, bestParams
}

func (r pluginRoute) specificity() []int {
	specificity := make([]int, len(r.segments))
	for index, segment := range r.segments {
		switch {
		case len(segment) > 1 && segment[0] == '*':
			specificity[index] = 0
		case len(segment) > 1 && segment[0] == ':':
			specificity[index] = 1
		default:
			specificity[index] = 2
		}
	}
	return specificity
}

func pluginRouteMoreSpecific(candidate, current []int) bool {
	for index := 0; index < len(candidate) && index < len(current); index++ {
		if candidate[index] != current[index] {
			return candidate[index] > current[index]
		}
	}
	return len(candidate) > len(current)
}

func (r pluginRoute) match(pathSegments []string) (gin.Params, bool) {
	params := make(gin.Params, 0)
	for index, segment := range r.segments {
		if len(segment) > 1 && segment[0] == '*' {
			if index != len(r.segments)-1 || index >= len(pathSegments) {
				return nil, false
			}
			return append(params, gin.Param{Key: segment[1:], Value: "/" + strings.Join(pathSegments[index:], "/")}), true
		}
		if index >= len(pathSegments) {
			return nil, false
		}
		if len(segment) > 1 && segment[0] == ':' {
			if pathSegments[index] == "" {
				return nil, false
			}
			params = append(params, gin.Param{Key: segment[1:], Value: pathSegments[index]})
			continue
		}
		if segment != pathSegments[index] {
			return nil, false
		}
	}
	return params, len(pathSegments) == len(r.segments)
}

func splitPluginPath(path string) []string {
	return strings.Split(strings.TrimPrefix(path, "/"), "/")
}

func normalizePluginPath(path string) string {
	path = "/" + strings.Trim(strings.TrimSpace(path), "/")
	if path == "/" {
		return path
	}
	return strings.TrimSuffix(path, "/")
}

func pluginRouteShape(path string) string {
	segments := splitPluginPath(path)
	for index, segment := range segments {
		if len(segment) > 1 && (segment[0] == ':' || segment[0] == '*') {
			segments[index] = segment[:1]
		}
	}
	return strings.Join(segments, "/")
}

// PluginManager 管理 .so 插件的加载与生命周期
type PluginManager struct {
	bear    *Bear
	plugins map[string]*loadedPlugin
	mu      sync.Mutex
}

type pluginRegistrationBarrier struct {
	mu            sync.Mutex
	registrations int
	blocked       bool
	transition    chan struct{}
	changed       chan struct{}
}

func newPluginRegistrationBarrier() *pluginRegistrationBarrier {
	return &pluginRegistrationBarrier{changed: make(chan struct{})}
}

func (b *pluginRegistrationBarrier) begin() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.blocked {
		return ErrPluginHotReloadUnsupported
	}
	b.registrations++
	return nil
}

func (b *pluginRegistrationBarrier) end() {
	b.mu.Lock()
	if b.registrations > 0 {
		b.registrations--
		close(b.changed)
		b.changed = make(chan struct{})
	}
	b.mu.Unlock()
}

func (b *pluginRegistrationBarrier) close(ctx context.Context) error {
	return b.closeWithCommit(ctx, nil)
}

func (b *pluginRegistrationBarrier) closeWithCommit(ctx context.Context, commit func()) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	for {
		b.mu.Lock()
		if b.blocked {
			if b.transition == nil {
				b.mu.Unlock()
				return nil
			}
			transition := b.transition
			b.mu.Unlock()
			select {
			case <-transition:
				continue
			case <-ctx.Done():
				return ctx.Err()
			}
		}

		b.blocked = true
		b.transition = make(chan struct{})
		transition := b.transition
		for b.registrations > 0 {
			changed := b.changed
			b.mu.Unlock()
			select {
			case <-changed:
				b.mu.Lock()
			case <-ctx.Done():
				b.mu.Lock()
				b.blocked = false
				b.transition = nil
				close(transition)
				b.mu.Unlock()
				return ctx.Err()
			}
		}
		if commit != nil {
			commit()
		}
		b.transition = nil
		close(transition)
		b.mu.Unlock()
		return nil
	}
}

type loadedPlugin struct {
	Module Module
	Symbol plugin.Symbol
}

func NewPluginManager(bear *Bear) *PluginManager {
	return &PluginManager{
		bear:    bear,
		plugins: make(map[string]*loadedPlugin),
	}
}

func (p *PluginManager) Load(path string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	return p.withRegistration(func() error {
		if err := validatePluginPathForConfig(path, p.bear.runtime.Config); err != nil {
			return err
		}

		pluginHandle, err := plugin.Open(path)
		if err != nil {
			return fmt.Errorf("failed to open plugin %s: %w", path, err)
		}

		sym, err := pluginHandle.Lookup("GetModule")
		if err != nil {
			return fmt.Errorf("plugin %s does not export 'GetModule' symbol", path)
		}

		getModule, ok := sym.(func() Module)
		if !ok {
			return errors.New("invalid 'GetModule' function signature in plugin")
		}

		mod := getModule()
		p.bear.runtime.Logger.Info("Loading dynamic module", "name", mod.Name(), "path", path)
		if err := p.registerModuleInRegistration(mod); err != nil {
			return err
		}
		p.plugins[path] = &loadedPlugin{Module: mod, Symbol: sym}
		return nil
	})
}

func (p *PluginManager) registerModule(mod Module) error {
	return p.withRegistration(func() error {
		return p.registerModuleInRegistration(mod)
	})
}

func (p *PluginManager) withRegistration(register func() error) error {
	if err := p.bear.beginPluginRegistration(); err != nil {
		return err
	}
	defer p.bear.endPluginRegistration()
	return register()
}

func (p *PluginManager) registerModuleInRegistration(mod Module) error {
	if p.bear.frameworkStrict() {
		if err := p.bear.addModulesE(false, mod); err != nil {
			return fmt.Errorf("register strict plugin module %T: %w", mod, err)
		}
		if err := p.bear.applyStrictObject(mod); err != nil {
			return fmt.Errorf("inject strict plugin module %T: %w", mod, err)
		}
		if err := p.bear.injectStrictContainerBeans(); err != nil {
			return fmt.Errorf("inject strict plugin module beans %T: %w", mod, err)
		}
		p.bear.pluginMode = true
		defer func() { p.bear.pluginMode = false }()
		if err := p.bear.buildModuleStrict(mod); err != nil {
			return err
		}
		p.bear.markStrictPluginModuleBuilt(mod)
		return nil
	}

	beans := mod.Beans()
	// 1. 注册 Beans 到主 IoC 容器
	for _, bean := range beans {
		if err := p.bear.runtime.Container.TrySet(bean); err != nil {
			return fmt.Errorf("register plugin bean %s: %w", bean.Name(), err)
		}
		// 自动执行依赖注入
		p.bear.runtime.Container.Apply(bean)
	}

	// 2. 构建模块路由
	// 我们需要将 Bear 切换到“插件模式”，使其路由注册到 PluginDispatcher
	p.bear.pluginMode = true
	defer func() { p.bear.pluginMode = false }()
	mod.Build(p.bear)
	return nil
}

func validatePluginPathForConfig(path string, config *SysConfig) error {
	if config == nil || config.Plugins == nil || !config.Plugins.Enabled {
		return errors.New("dynamic plugin loading is disabled")
	}
	if len(config.Plugins.AllowedDirs) == 0 {
		return errors.New("dynamic plugin loading requires allowed plugin directories")
	}

	absPath, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("invalid plugin path %s: %w", path, err)
	}
	cleanPath, err := filepath.EvalSymlinks(absPath)
	if err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("invalid plugin path %s: %w", path, err)
		}
		cleanPath = absPath
	}

	for _, dir := range config.Plugins.AllowedDirs {
		absDir, err := filepath.Abs(dir)
		if err != nil {
			return fmt.Errorf("invalid plugin directory %s: %w", dir, err)
		}
		cleanDir, err := filepath.EvalSymlinks(absDir)
		if err != nil {
			if !os.IsNotExist(err) {
				return fmt.Errorf("invalid plugin directory %s: %w", dir, err)
			}
			cleanDir = absDir
		}
		if cleanPath == cleanDir {
			return fmt.Errorf("plugin path %s is not a file", path)
		}
		if rel, err := filepath.Rel(cleanDir, cleanPath); err == nil && rel != "." && !stringsHasPathTraversal(rel) {
			return nil
		}
	}

	return fmt.Errorf("plugin path %s is not allowed", path)
}

func stringsHasPathTraversal(rel string) bool {
	return rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}

func (p *PluginManager) Reload(path string) error {
	// Go plugin 无法真正从内存卸载，Reload 实际上是加载新版本并覆盖分发器映射
	// 注意：旧版本的对象可能仍然在内存中，但分发器会指向新版本
	return p.Load(path)
}
