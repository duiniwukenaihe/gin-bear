package bear

import (
	"errors"
	"fmt"
	"log/slog"
	"plugin"
	"sync"

	"github.com/gin-gonic/gin"
)

// PluginDispatcher 负责动态路由分发
type PluginDispatcher struct {
	// handlers[method][path] = HandlerFunc
	handlers map[string]map[string]gin.HandlerFunc
	mu       sync.RWMutex
}

func NewPluginDispatcher() *PluginDispatcher {
	return &PluginDispatcher{
		handlers: make(map[string]map[string]gin.HandlerFunc),
	}
}

func (this *PluginDispatcher) Register(method, path string, handler gin.HandlerFunc) {
	this.mu.Lock()
	defer this.mu.Unlock()
	if _, ok := this.handlers[method]; !ok {
		this.handlers[method] = make(map[string]gin.HandlerFunc)
	}
	this.handlers[method][path] = handler
	slog.Info("Dynamic route registered", "method", method, "path", path)
}

func (this *PluginDispatcher) Unregister(method, path string) {
	this.mu.Lock()
	defer this.mu.Unlock()
	if m, ok := this.handlers[method]; ok {
		delete(m, path)
	}
}

func (this *PluginDispatcher) Dispatch() gin.HandlerFunc {
	return func(ctx *gin.Context) {
		this.mu.RLock()
		method := ctx.Request.Method
		path := ctx.FullPath()
		if path == "" {
			path = ctx.Request.URL.Path
		}

		if m, ok := this.handlers[method]; ok {
			if handler, exists := m[path]; exists {
				this.mu.RUnlock()
				handler(ctx)
				ctx.Abort()
				return
			}
		}
		this.mu.RUnlock()
		ctx.Next()
	}
}

// PluginManager 管理 .so 插件的加载与生命周期
type PluginManager struct {
	bear    *Bear
	plugins map[string]*loadedPlugin
	mu      sync.Mutex
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

func (this *PluginManager) Load(path string) error {
	this.mu.Lock()
	defer this.mu.Unlock()

	p, err := plugin.Open(path)
	if err != nil {
		return fmt.Errorf("failed to open plugin %s: %w", path, err)
	}

	sym, err := p.Lookup("GetModule")
	if err != nil {
		return fmt.Errorf("plugin %s does not export 'GetModule' symbol", path)
	}

	getModule, ok := sym.(func() Module)
	if !ok {
		return errors.New("invalid 'GetModule' function signature in plugin")
	}

	mod := getModule()
	slog.Info("Loading dynamic module", "name", mod.Name(), "path", path)

	// 1. 注册 Beans 到主 IoC 容器
	for _, bean := range mod.Beans() {
		GetInjector().Set(bean)
		// 自动执行依赖注入
		GetInjector().Apply(bean)
	}

	// 2. 构建模块路由
	// 我们需要将 Bear 切换到“插件模式”，使其路由注册到 PluginDispatcher
	this.bear.pluginMode = true
	mod.Build(this.bear)
	this.bear.pluginMode = false

	this.plugins[path] = &loadedPlugin{Module: mod, Symbol: sym}
	return nil
}

func (this *PluginManager) Reload(path string) error {
	// Go plugin 无法真正从内存卸载，Reload 实际上是加载新版本并覆盖分发器映射
	// 注意：旧版本的对象可能仍然在内存中，但分发器会指向新版本
	return this.Load(path)
}
