//go:build !bear_no_casbin

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

// CasbinEnforcer 是 Casbin CachedEnforcer 的包装，实现 Bean 接口。
// 工厂默认关闭决策缓存以保证撤权立即生效；公开嵌入字段为兼容保留，
// 不保证并发鉴权与策略写入安全。
type CasbinEnforcer struct {
	*casbin.CachedEnforcer
}

func (c *CasbinEnforcer) Name() string {
	return "CasbinEnforcer"
}

// buildCasbinModel 构造 Casbin 模型，每个调用者持有独立实例。
// 由 NewCasbinEnforcer 与 NewCasbinAuthorizer 共享，避免可变模型对象在实例间共享。
func buildCasbinModel(cfg *CasbinConfig) (model.Model, error) {
	if cfg != nil && cfg.ModelText != "" {
		m, err := model.NewModelFromString(cfg.ModelText)
		if err != nil {
			return nil, fmt.Errorf("failed to create Casbin model: %w", err)
		}
		return m, nil
	}
	if cfg != nil && cfg.ModelPath != "" {
		m, err := model.NewModelFromFile(cfg.ModelPath)
		if err != nil {
			return nil, fmt.Errorf("failed to create Casbin model: %w", err)
		}
		return m, nil
	}
	// 默认 RBAC 模型，支持 RESTful 路径匹配 (keyMatch)
	const defaultModel = `
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
	m, err := model.NewModelFromString(defaultModel)
	if err != nil {
		return nil, fmt.Errorf("failed to create Casbin model: %w", err)
	}
	return m, nil
}

// NewCasbinEnforcer 初始化一个新的 Casbin 执行器，默认关闭决策缓存。
func NewCasbinEnforcer(adapter *GormAdapter, cfg *CasbinConfig) (*CasbinEnforcer, error) {
	m, err := buildCasbinModel(cfg)
	if err != nil {
		return nil, err
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

	// 默认关闭决策缓存，保证撤权立即生效。
	// CachedEnforcer 并不会为所有角色、命名策略、过滤删除等操作完整清理
	// 决策缓存；单独重写某个删除方法或增加 TTL 都不能满足立即撤权。
	// 旧接口保留公开嵌入字段与构造签名以兼容 v0.9.1 API 基线，但默认不再
	// 使用决策缓存。调用者显式重新开启缓存属于自定义行为，不在默认撤权
	// 保证内；手工构造 CasbinEnforcer{CachedEnforcer: ...} 也不自动取得
	// 工厂的默认保证。旧接口仍不保证“请求鉴权与策略写入并发执行”安全，
	// 需要并发鉴权与在线策略变更请使用 CasbinAuthorizer。
	e.EnableCache(false)

	// 只有当有适配器时才需要加载策略，内存模式不需要显式 LoadPolicy
	if adapter != nil {
		if err = e.LoadPolicy(); err != nil {
			return nil, fmt.Errorf("failed to load Casbin policy: %w", err)
		}
	}

	slog.Info("Casbin enforcer initialized successfully (decision cache disabled by default)")
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
		slog.ErrorContext(ctx.Request.Context(), "Casbin enforcer is not injected",
			"user", sub,
			"path", obj,
			"method", act,
		)
		return ErrInternalServer
	}
	allowed, err := c.Enforcer.Enforce(sub, obj, act)
	if err != nil {
		slog.ErrorContext(ctx.Request.Context(), "Casbin enforcement failed",
			"error", err,
			"user", sub,
			"path", obj,
			"method", act,
		)
		return ErrInternalServer
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
