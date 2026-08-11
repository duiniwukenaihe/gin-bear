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

// PluginDispatcher 负责动态路由分发
type PluginDispatcher struct {
	// handlers[method][path] = HandlerFunc
	handlers map[string]map[string]gin.HandlerFunc
	mu       sync.RWMutex
	logger   *slog.Logger
}

func NewPluginDispatcher() *PluginDispatcher {
	return newPluginDispatcher(nil)
}

func newPluginDispatcher(logger *slog.Logger) *PluginDispatcher {
	return &PluginDispatcher{
		handlers: make(map[string]map[string]gin.HandlerFunc),
		logger:   logger,
	}
}

func (p *PluginDispatcher) Register(method, path string, handler gin.HandlerFunc) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, ok := p.handlers[method]; !ok {
		p.handlers[method] = make(map[string]gin.HandlerFunc)
	}
	p.handlers[method][path] = handler
	logger := p.logger
	if logger == nil {
		logger = legacyLogger()
	}
	logger.Info("Dynamic route registered", "method", method, "path", path)
}

func (p *PluginDispatcher) Unregister(method, path string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if m, ok := p.handlers[method]; ok {
		delete(m, path)
	}
}

func (p *PluginDispatcher) Dispatch() gin.HandlerFunc {
	return func(ctx *gin.Context) {
		p.mu.RLock()
		method := ctx.Request.Method
		path := ctx.FullPath()
		if path == "" {
			path = ctx.Request.URL.Path
		}

		if m, ok := p.handlers[method]; ok {
			if handler, exists := m[path]; exists {
				p.mu.RUnlock()
				handler(ctx)
				ctx.Abort()
				return
			}
		}
		p.mu.RUnlock()
		ctx.Next()
	}
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
