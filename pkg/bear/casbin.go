package bear

import (
	"fmt"
	"log/slog"
	"strconv"

	"github.com/casbin/casbin/v2"
	"github.com/casbin/casbin/v2/model"
	gormadapter "github.com/casbin/gorm-adapter/v3"
	"github.com/gin-gonic/gin"
)

// CasbinConfig 提供 Casbin 的配置
type CasbinConfig struct {
	ModelPath string `yaml:"model_path"`
	ModelText string `yaml:"model_text"`
}

// CasbinEnforcer 是 Casbin CachedEnforcer 的包装，实现 Bean 接口
type CasbinEnforcer struct {
	*casbin.CachedEnforcer
}

func (c *CasbinEnforcer) Name() string {
	return "CasbinEnforcer"
}

// NewCasbinEnforcer 初始化一个新的 Casbin 执行器 (支持缓存)
func NewCasbinEnforcer(adapter *GormAdapter, cfg *CasbinConfig) (*CasbinEnforcer, error) {
	var m model.Model
	var err error

	if cfg != nil && cfg.ModelText != "" {
		m, err = model.NewModelFromString(cfg.ModelText)
	} else if cfg != nil && cfg.ModelPath != "" {
		m, err = model.NewModelFromFile(cfg.ModelPath)
	} else {
		// 默认 RBAC 模型，支持 RESTful 路径匹配 (keyMatch)
		defaultModel := `
[request_definition]
r = sub, obj, act

[policy_definition]
p = sub, obj, act

[role_definition]
g = _, _

[policy_effect]
e = some(where (p.eft == allow))

[matchers]
m = g(r.sub, p.sub) && keyMatch(r.obj, p.obj) && (r.act == p.act || p.act == "*")
`
		m, err = model.NewModelFromString(defaultModel)
	}

	if err != nil {
		return nil, fmt.Errorf("failed to create Casbin model: %w", err)
	}

	var e *casbin.CachedEnforcer
	if adapter != nil {
		// 使用 GORM 适配器实现持久化存储
		a, adapterErr := gormadapter.NewAdapterByDB(adapter.DB)
		if adapterErr != nil {
			return nil, fmt.Errorf("failed to create Casbin adapter: %w", adapterErr)
		}
		e, err = casbin.NewCachedEnforcer(m, a)
	} else {
		// 使用内存适配器
		e, err = casbin.NewCachedEnforcer(m)
	}

	if err != nil {
		return nil, fmt.Errorf("failed to create Casbin enforcer: %w", err)
	}

	// 启用缓存失效机制 (如果需要手动管理缓存，可以调用 e.InvalidateCache())
	// 默认情况下，CachedEnforcer 会在加载策略后自动管理缓存

	// 只有当有适配器时才需要加载策略，内存模式不需要显式 LoadPolicy
	if adapter != nil {
		if err = e.LoadPolicy(); err != nil {
			return nil, fmt.Errorf("failed to load Casbin policy: %w", err)
		}
	}

	slog.Info("Casbin cached enforcer initialized successfully")
	return &CasbinEnforcer{CachedEnforcer: e}, nil
}

// CasbinFairing 是用于 RBAC 权限检查的拦截器
type CasbinFairing struct {
	BaseFairing
	Enforcer *CasbinEnforcer `inject:"-"`
}

func NewCasbinFairing() *CasbinFairing {
	return &CasbinFairing{}
}

func (c *CasbinFairing) OnRequest(ctx *gin.Context) error {
	// 从上下文中获取用户身份 (由 AuthFairing 设置)
	userID, exists := ctx.Get("current_user_id")
	if !exists {
		// 如果没有认证信息，视为匿名用户
		userID = "anonymous"
	}

	sub := ""
	switch v := userID.(type) {
	case uint:
		sub = strconv.FormatUint(uint64(v), 10)
	case int:
		sub = strconv.Itoa(v)
	default:
		sub = fmt.Sprintf("%v", v)
	}

	obj := ctx.Request.URL.Path
	act := ctx.Request.Method

	// 执行权限检查
	if c.Enforcer == nil {
		// 尝试手动从容器获取
		c.Enforcer = GetByType[*CasbinEnforcer]()
	}
	if c.Enforcer == nil {
		slog.Error("CasbinEnforcer is nil, please ensure it is injected")
		return NewError(500, "internal server error: CasbinEnforcer not ready")
	}
	allowed, err := c.Enforcer.Enforce(sub, obj, act)
	if err != nil {
		return NewError(500, fmt.Sprintf("Casbin enforcement error: %v", err))
	}

	if !allowed {
		slog.WarnContext(ctx.Request.Context(), "Access denied", "user", sub, "path", obj, "method", act)
		return NewError(403, "access denied: insufficient permissions")
	}

	return nil
}

func (c *CasbinFairing) Name() string {
	return "CasbinFairing"
}
