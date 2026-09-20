package bear

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/casbin/casbin/v2"
	"github.com/casbin/casbin/v2/model"
	gormadapter "github.com/casbin/gorm-adapter/v3"
)

// CasbinAuthorizer 是在线策略变更场景的受控鉴权入口。
//
// 与保留兼容的 CasbinEnforcer（公开嵌入 CachedEnforcer）不同，本类型内部只
// 保存私有普通 *casbin.Enforcer、私有读写锁和策略不可用状态：不用
// CachedEnforcer，不导出底层对象，不提供任意修改回调，不再套一层
// SyncedEnforcer 形成重复锁体系。
//
// 并发契约：Authorize 在读锁内检查可用状态并执行 Enforce；所有写操作和
// LoadPolicy 在同一把写锁内执行。撤权方法成功返回后才开始的新鉴权必须看到
// 新策略；已经在撤权之前完成授权的在途业务不在追溯取消范围内。
//
// 失败关闭：后端持久化/加载失败时返回错误并将该 authorizer 标为不可用，
// 后续鉴权返回错误，不使用可能陈旧的允许策略；成功 LoadPolicy 后才恢复。
// “不存在、未改变”的 false, nil 不是故障。写入前按模型校验策略与角色关系
// 的参数数量，非法输入直接拒绝，不修改内存、不触碰数据库、不标记不可用。
//
// 本版本只支持三个请求参数的 RBAC 模型（Subject, Resource, Action），
// 构造时拒绝不匹配模型；非空 Scope 明确返回不支持错误，不静默丢弃项目/
// 租户范围。需要 ABAC/domain 的应用继续提供自定义 Authorizer。
//
// Casbin 内存计算及普通锁等待本身不是可被 context 强制中断的任务，因此只
// 在入口检查调用上下文取消，不额外开启 goroutine 制造“超时后仍在后台运行”。
type CasbinAuthorizer struct {
	enforcer    *casbin.Enforcer
	mu          sync.RWMutex
	unavailable bool
	// hasAdapter 报告是否存在持久化后端；内存模式拒绝重载但保持可用。
	hasAdapter bool
	// policyLen/groupingLen 是模型要求的参数数量；写入前校验，非法输入
	// 不得修改内存或数据库。无角色定义的模型记 groupingLen 为 -1。
	policyLen   int
	groupingLen int
}

// Name 标识该 Bean。
func (a *CasbinAuthorizer) Name() string {
	return "CasbinAuthorizer"
}

var _ Authorizer = (*CasbinAuthorizer)(nil)

// ErrCasbinReloadRequiresAdapter 标识内存模式重载：没有持久化适配器时
// LoadPolicy 无法重载，该错误可被 errors.Is 识别；内存策略保持可用。
var ErrCasbinReloadRequiresAdapter = errors.New("casbin reload requires a persistent adapter")

// NewCasbinAuthorizer 构造受控鉴权入口。每个 authorizer 拥有独立模型及内存
// 策略，不共享可变模型对象。
func NewCasbinAuthorizer(adapter *GormAdapter, cfg *CasbinConfig) (*CasbinAuthorizer, error) {
	m, err := buildCasbinModel(cfg)
	if err != nil {
		return nil, err
	}
	if err := requireTripleRBACModel(m); err != nil {
		return nil, err
	}
	policyLen, groupingLen, err := ruleArity(m)
	if err != nil {
		return nil, err
	}
	authorizer := &CasbinAuthorizer{hasAdapter: adapter != nil, policyLen: policyLen, groupingLen: groupingLen}
	var e *casbin.Enforcer
	if adapter != nil {
		a, adapterErr := gormadapter.NewAdapterByDB(adapter.DB)
		if adapterErr != nil {
			return nil, fmt.Errorf("failed to create Casbin adapter: %w", adapterErr)
		}
		e, err = casbin.NewEnforcer(m, a)
		if err != nil {
			return nil, fmt.Errorf("failed to create Casbin enforcer: %w", err)
		}
		if err = e.LoadPolicy(); err != nil {
			return nil, fmt.Errorf("failed to load Casbin policy: %w", err)
		}
	} else {
		e, err = casbin.NewEnforcer(m)
		if err != nil {
			return nil, fmt.Errorf("failed to create Casbin enforcer: %w", err)
		}
	}
	slog.Info("Casbin authorizer initialized successfully")
	authorizer.enforcer = e
	return authorizer, nil
}

// modelTokens 读取模型断言的参数数量；缺失不断言 panic，而是返回错误。
func modelTokens(m model.Model, section, ptype string) (int, error) {
	assertions, ok := m[section]
	if !ok {
		return 0, fmt.Errorf("CasbinAuthorizer model has no %q section", section)
	}
	assertion, ok := assertions[ptype]
	if !ok || assertion == nil {
		return 0, fmt.Errorf("CasbinAuthorizer model has no %q %q definition", section, ptype)
	}
	return len(assertion.Tokens), nil
}

// requireTripleRBACModel 拒绝非三元请求/策略模型的构造。
func requireTripleRBACModel(m model.Model) error {
	requestTokens, err := modelTokens(m, "r", "r")
	if err != nil {
		return err
	}
	policyTokens, err := modelTokens(m, "p", "p")
	if err != nil {
		return err
	}
	if requestTokens != 3 || policyTokens != 3 {
		return fmt.Errorf("CasbinAuthorizer requires a 3-parameter RBAC model (r tokens=%d, p tokens=%d)", requestTokens, policyTokens)
	}
	return nil
}

// ruleArity 返回策略与角色关系的参数数量；无角色定义的模型返回 groupingLen -1。
func ruleArity(m model.Model) (policyLen, groupingLen int, err error) {
	if policyLen, err = modelTokens(m, "p", "p"); err != nil {
		return 0, 0, err
	}
	groupingLen, err = modelTokens(m, "g", "g")
	if err != nil {
		return policyLen, -1, nil
	}
	return policyLen, groupingLen, nil
}

// checkRule 在写入前校验参数数量；非法输入直接拒绝，不持有锁、不修改
// 内存、不触碰数据库，也不会触发不可用标记。
func (a *CasbinAuthorizer) checkRule(grouping bool, rule []string) error {
	want, kind := a.policyLen, "policy"
	if grouping {
		if a.groupingLen < 0 {
			return fmt.Errorf("casbin grouping policies are not supported by this model")
		}
		want, kind = a.groupingLen, "grouping policy"
	}
	if len(rule) != want {
		return fmt.Errorf("casbin %s requires %d parameters, got %d", kind, want, len(rule))
	}
	return nil
}

// Authorize 执行一次资源级鉴权决定。
func (a *CasbinAuthorizer) Authorize(ctx context.Context, request AuthorizationRequest) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if len(request.Scope) > 0 {
		return false, fmt.Errorf("CasbinAuthorizer does not support scoped authorization (scope has %d entries)", len(request.Scope))
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.unavailable {
		return false, fmt.Errorf("casbin policy is unavailable: refusing to authorize with a possibly stale policy")
	}
	allowed, err := a.enforcer.Enforce(request.Subject, request.Resource, request.Action)
	if err != nil {
		return false, err
	}
	return allowed, nil
}

// stringParams 把调用参数规范化为 Casbin 策略字符串；非字符串参数明确拒绝
// 而不是触发底层类型断言 panic，也不静默改写调用者语义。
func stringParams(params []any) ([]string, error) {
	converted := make([]string, 0, len(params))
	for i, param := range params {
		text, ok := param.(string)
		if !ok {
			return nil, fmt.Errorf("casbin policy parameter %d has type %T, want string", i, param)
		}
		converted = append(converted, text)
	}
	return converted, nil
}

// AddPolicy 增加一条策略。重复（false, nil）不是故障，不标记不可用。
func (a *CasbinAuthorizer) AddPolicy(params ...any) (bool, error) {
	rule, err := stringParams(params)
	if err != nil {
		return false, err
	}
	if err := a.checkRule(false, rule); err != nil {
		return false, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	added, err := a.enforcer.AddPolicy(rule)
	if err != nil {
		a.unavailable = true
		return false, err
	}
	return added, nil
}

// RemovePolicy 删除一条策略。
func (a *CasbinAuthorizer) RemovePolicy(params ...any) (bool, error) {
	rule, err := stringParams(params)
	if err != nil {
		return false, err
	}
	if err := a.checkRule(false, rule); err != nil {
		return false, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	removed, err := a.enforcer.RemovePolicy(rule)
	if err != nil {
		a.unavailable = true
		return false, err
	}
	return removed, nil
}

// AddGroupingPolicy 增加一条角色继承关系。
func (a *CasbinAuthorizer) AddGroupingPolicy(params ...any) (bool, error) {
	rule, err := stringParams(params)
	if err != nil {
		return false, err
	}
	if err := a.checkRule(true, rule); err != nil {
		return false, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	added, err := a.enforcer.AddGroupingPolicy(rule)
	if err != nil {
		a.unavailable = true
		return false, err
	}
	return added, nil
}

// RemoveGroupingPolicy 删除一条角色继承关系（撤权路径）。
func (a *CasbinAuthorizer) RemoveGroupingPolicy(params ...any) (bool, error) {
	rule, err := stringParams(params)
	if err != nil {
		return false, err
	}
	if err := a.checkRule(true, rule); err != nil {
		return false, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	removed, err := a.enforcer.RemoveGroupingPolicy(rule)
	if err != nil {
		a.unavailable = true
		return false, err
	}
	return removed, nil
}

// LoadPolicy 从持久化后端重载策略；成功后恢复可用状态，失败则标记不可用。
// 内存模式没有可重载的后端，返回 ErrCasbinReloadRequiresAdapter 且保持现有
// 策略可用，不 panic。
func (a *CasbinAuthorizer) LoadPolicy() error {
	if !a.hasAdapter {
		return ErrCasbinReloadRequiresAdapter
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.enforcer.LoadPolicy(); err != nil {
		a.unavailable = true
		return err
	}
	a.unavailable = false
	return nil
}
