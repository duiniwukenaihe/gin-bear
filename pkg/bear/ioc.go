package bear

import (
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"
)

var (
	// ErrBeanMissing reports an unresolved strict dependency.
	ErrBeanMissing = errors.New("bean missing")
	// ErrBeanAmbiguous reports more than one implicit strict dependency.
	ErrBeanAmbiguous = errors.New("bean ambiguous")
	// ErrBeanDuplicate reports two different instances registered for one concrete type.
	ErrBeanDuplicate = errors.New("bean duplicate")
)

// BeanFactory 负责管理所有的 Bean
type BeanFactory struct {
	mu         sync.RWMutex
	beans      map[reflect.Type]any
	order      []reflect.Type
	concrete   map[reflect.Type]any
	conflicts  map[reflect.Type]struct{}
	strict     bool
	onSet      func(reflect.Type, any, func()) error
	onBatchSet func([]beanRegistration, func()) error
	onRemove   func(reflect.Type, ...func()) error
}

type beanRegistration struct {
	beanType reflect.Type
	bean     any
}

var bootstrapInjector = NewBeanFactory()

// StaticInjector 静态注入函数定义
type StaticInjector func(interface{})

// RuntimeStaticInjector resolves dependencies from the BeanFactory that owns
// the object being injected.
type RuntimeStaticInjector func(*BeanFactory, interface{})

// RuntimeStaticInjectorE is a container-scoped generated injector that can
// report strict injection failures.
type RuntimeStaticInjectorE func(*BeanFactory, any) error

var staticInjectors = make(map[string]StaticInjector)
var runtimeStaticInjectors = make(map[string]RuntimeStaticInjector)
var runtimeStaticInjectorsE = make(map[string]RuntimeStaticInjectorE)
var staticMu sync.RWMutex

func init() {
	RegisterRuntimeStaticInjectorE(runtimeStaticInjectorKey(reflect.TypeFor[JWTUtil]()), func(factory *BeanFactory, obj any) error {
		target, ok := obj.(*JWTUtil)
		if !ok {
			return fmt.Errorf("strict static injector received %T, want *bear.JWTUtil", obj)
		}
		if target.Config != nil {
			return nil
		}
		config, err := ResolveE[*SysConfig](factory)
		if err != nil {
			return fmt.Errorf("resolve JWT configuration: %w", err)
		}
		target.Config = newJWTUtilFromAuthConfig(config.Auth).Config
		return nil
	})
}

// RegisterStaticInjector 注册静态注入器
func RegisterStaticInjector(name string, injector StaticInjector) {
	staticMu.Lock()
	defer staticMu.Unlock()
	staticInjectors[name] = injector
}

// RegisterRuntimeStaticInjector registers a container-scoped generated injector.
func RegisterRuntimeStaticInjector(name string, injector RuntimeStaticInjector) {
	staticMu.Lock()
	defer staticMu.Unlock()
	runtimeStaticInjectors[name] = injector
}

// RegisterRuntimeStaticInjectorE registers a strict generated injector under
// the full package-path-and-type-name key returned by runtimeStaticInjectorKey.
func RegisterRuntimeStaticInjectorE(key string, injector RuntimeStaticInjectorE) {
	staticMu.Lock()
	defer staticMu.Unlock()
	runtimeStaticInjectorsE[key] = injector
}

func runtimeStaticInjectorKey(t reflect.Type) string {
	for t != nil && t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	if t == nil {
		return ""
	}
	return t.PkgPath() + "." + t.Name()
}

// GetInjector 获取单例注入器
func GetInjector() *BeanFactory {
	return getInjector(nil)
}

func getInjector(beforeBootstrapPublish func()) *BeanFactory {
	for {
		facade := loadDefaultFacade()
		if facade != nil && facade.injector != nil {
			return facade.injector
		}
		if beforeBootstrapPublish != nil {
			beforeBootstrapPublish()
			beforeBootstrapPublish = nil
		}
		var bootstrapFacade legacyFacade
		if facade != nil {
			bootstrapFacade = *facade
		}
		bootstrapFacade.injector = bootstrapInjector
		if defaultFacade.CompareAndSwap(facade, &bootstrapFacade) {
			return bootstrapInjector
		}
	}
}

func setDefaultInjector(factory *BeanFactory) {
	if factory == nil {
		factory = NewBeanFactory()
	}
	updateDefaultFacade(func(facade legacyFacade) legacyFacade {
		facade.injector = factory
		return facade
	})
}

// NewBeanFactory creates an isolated bean container.
func NewBeanFactory() *BeanFactory {
	return &BeanFactory{
		beans:     make(map[reflect.Type]any),
		concrete:  make(map[reflect.Type]any),
		conflicts: make(map[reflect.Type]struct{}),
	}
}

// Resolve retrieves a bean from the provided container.
func Resolve[T any](factory *BeanFactory) T {
	var zero T
	if factory == nil {
		return zero
	}
	value, _ := factory.Get(reflect.TypeOf((*T)(nil)).Elem()).(T)
	return value
}

// ResolveE retrieves a dependency without choosing between implicit candidates.
func ResolveE[T any](factory *BeanFactory) (T, error) {
	var zero T
	requestedType := reflect.TypeFor[T]()
	if factory == nil {
		return zero, fmt.Errorf("%w: dependency %s", ErrBeanMissing, requestedType)
	}
	bean, err := factory.resolveE(requestedType)
	if err != nil {
		return zero, err
	}
	value, ok := bean.(T)
	if !ok {
		return zero, fmt.Errorf("%w: dependency %s", ErrBeanMissing, requestedType)
	}
	return value, nil
}

// Set 注册一个 Bean
func (f *BeanFactory) Set(bean any) {
	if f == nil || bean == nil {
		return
	}
	v := reflect.ValueOf(bean)
	f.mu.Lock()
	defer f.mu.Unlock()
	_ = f.registerLocked(v.Type(), bean, false)
}

// TrySet registers a bean or reports that the owning lifecycle is closed.
func (f *BeanFactory) TrySet(bean any) error {
	if f == nil {
		return nil
	}
	if bean == nil || isNilBean(bean) {
		return fmt.Errorf("bean must not be nil")
	}
	v := reflect.ValueOf(bean)
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.registerLocked(v.Type(), bean, true)
}

func (f *BeanFactory) trySetBatchStrict(beans []any) error {
	if f == nil {
		return fmt.Errorf("bean factory is nil")
	}
	for index, bean := range beans {
		if bean == nil || isNilBean(bean) {
			return fmt.Errorf("strict bean batch item %d must not be nil", index)
		}
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	plannedBeans := make(map[reflect.Type]any, len(f.beans)+len(beans))
	for beanType, bean := range f.beans {
		plannedBeans[beanType] = bean
	}
	plannedConcrete := make(map[reflect.Type]any, len(f.concrete)+len(beans))
	for beanType, bean := range f.concrete {
		plannedConcrete[beanType] = bean
	}
	registrations := make([]beanRegistration, 0, len(beans))
	for _, bean := range beans {
		beanType := reflect.TypeOf(bean)
		if current, exists := plannedBeans[beanType]; exists {
			if sameBeanInstance(current, bean) {
				continue
			}
			return fmt.Errorf("%w: concrete bean type %s", ErrBeanDuplicate, beanType)
		}
		if current, exists := plannedConcrete[beanType]; exists && !sameBeanInstance(current, bean) {
			return fmt.Errorf("%w: concrete bean type %s", ErrBeanDuplicate, beanType)
		}
		plannedBeans[beanType] = bean
		plannedConcrete[beanType] = bean
		registrations = append(registrations, beanRegistration{beanType: beanType, bean: bean})
	}
	commit := func() {
		for _, registration := range registrations {
			f.order = append(f.order, registration.beanType)
			f.beans[registration.beanType] = registration.bean
			f.concrete[registration.beanType] = registration.bean
		}
	}
	if f.onBatchSet != nil {
		return f.onBatchSet(registrations, commit)
	}
	if f.onSet != nil {
		return fmt.Errorf("strict batch registration requires an atomic lifecycle callback")
	}
	commit()
	return nil
}

func (f *BeanFactory) registerLocked(beanType reflect.Type, bean any, enforceStrict bool) error {
	if current, exists := f.beans[beanType]; exists && sameBeanInstance(current, bean) {
		return nil
	}
	concreteType := reflect.TypeOf(bean)
	previous, knownConcrete := f.concrete[concreteType]
	conflict := knownConcrete && !sameBeanInstance(previous, bean)
	if conflict && enforceStrict && f.strict {
		return fmt.Errorf("%w: concrete bean type %s", ErrBeanDuplicate, concreteType)
	}
	commit := func() {
		if _, exists := f.beans[beanType]; !exists {
			f.order = append(f.order, beanType)
		}
		f.beans[beanType] = bean
		f.concrete[concreteType] = bean
		if conflict {
			f.conflicts[concreteType] = struct{}{}
		}
	}
	if f.onSet != nil {
		return f.onSet(beanType, bean, commit)
	}
	commit()
	return nil
}

func sameBeanInstance(left, right any) bool {
	if reflect.TypeOf(left) != reflect.TypeOf(right) {
		return false
	}
	leftValue := reflect.ValueOf(left)
	if !leftValue.IsValid() {
		return false
	}
	if leftValue.Type().Comparable() {
		return leftValue.Interface() == reflect.ValueOf(right).Interface()
	}
	switch leftValue.Kind() {
	case reflect.Map, reflect.Slice:
		return leftValue.UnsafePointer() == reflect.ValueOf(right).UnsafePointer()
	default:
		return false
	}
}

// SetWithInterface 注册一个 Bean 并绑定到指定接口类型
// ifacePtr 必须是指向接口的指针，例如 (*MyInterface)(nil)
func (f *BeanFactory) SetWithInterface(ifacePtr any, bean any) {
	_ = f.TrySetWithInterface(ifacePtr, bean)
}

// TrySetWithInterface registers a bean for an interface or reports rejection.
func (f *BeanFactory) TrySetWithInterface(ifacePtr any, bean any) error {
	if f == nil {
		return fmt.Errorf("bean factory is nil")
	}
	ifaceType := reflect.TypeOf(ifacePtr)
	if ifaceType == nil || ifaceType.Kind() != reflect.Ptr || ifaceType.Elem().Kind() != reflect.Interface {
		return fmt.Errorf("ifacePtr must be a pointer to an interface")
	}
	beanType := reflect.TypeOf(bean)
	if beanType == nil || isNilBean(bean) {
		return fmt.Errorf("bean must not be nil")
	}
	interfaceType := ifaceType.Elem()
	if !beanType.Implements(interfaceType) {
		return fmt.Errorf("bean type %s does not implement interface %s", beanType, interfaceType)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.registerLocked(interfaceType, bean, true)
}

func isNilBean(bean any) bool {
	value := reflect.ValueOf(bean)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Ptr, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

// Remove 移除一个 Bean
func (f *BeanFactory) Remove(t reflect.Type) {
	_ = f.TryRemove(t)
}

// TryRemove removes a bean or reports that the owning lifecycle is closed.
func (f *BeanFactory) TryRemove(t reflect.Type) error {
	if f == nil {
		return nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	commit := func() {
		delete(f.beans, t)
		for i, registeredType := range f.order {
			if registeredType == t {
				f.order = append(f.order[:i], f.order[i+1:]...)
				break
			}
		}
		f.rebuildConcreteIndexesLocked()
	}
	if f.onRemove != nil {
		return f.onRemove(t, commit)
	}
	commit()
	return nil
}

func (f *BeanFactory) rebuildConcreteIndexesLocked() {
	clear(f.concrete)
	clear(f.conflicts)
	for _, registeredType := range f.order {
		bean, exists := f.beans[registeredType]
		if !exists {
			continue
		}
		concreteType := reflect.TypeOf(bean)
		if concreteType == nil {
			continue
		}
		if current, exists := f.concrete[concreteType]; exists && !sameBeanInstance(current, bean) {
			f.conflicts[concreteType] = struct{}{}
		}
		f.concrete[concreteType] = bean
	}
}

// Get 获取指定类型的 Bean
func (f *BeanFactory) Get(t reflect.Type) any {
	f.mu.RLock()
	defer f.mu.RUnlock()

	if v, ok := f.beans[t]; ok {
		return v
	}

	// 如果是接口类型，尝试进行接口实现匹配
	if t.Kind() == reflect.Interface {
		for _, registeredType := range f.order {
			bean := f.beans[registeredType]
			bt := reflect.TypeOf(bean)
			if bt.Implements(t) {
				return bean
			}
		}
	}
	return nil
}

func (f *BeanFactory) resolveE(requestedType reflect.Type) (any, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	if bean, ok := f.beans[requestedType]; ok {
		return bean, nil
	}
	if requestedType.Kind() != reflect.Interface {
		return nil, fmt.Errorf("%w: dependency %s", ErrBeanMissing, requestedType)
	}

	candidates := make(map[reflect.Type]any)
	for registeredType, bean := range f.beans {
		if registeredType.Kind() == reflect.Interface {
			continue
		}
		beanType := reflect.TypeOf(bean)
		if beanType != nil && beanType.Implements(requestedType) {
			candidates[beanType] = bean
		}
	}
	switch len(candidates) {
	case 0:
		return nil, fmt.Errorf("%w: dependency %s", ErrBeanMissing, requestedType)
	case 1:
		for _, bean := range candidates {
			return bean, nil
		}
	}
	return nil, fmt.Errorf("%w: dependency %s has %d implicit implementations", ErrBeanAmbiguous, requestedType, len(candidates))
}

func (f *BeanFactory) strictConflictError() error {
	if f == nil {
		return nil
	}
	f.mu.RLock()
	defer f.mu.RUnlock()
	if len(f.conflicts) == 0 {
		return nil
	}
	types := make([]string, 0, len(f.conflicts))
	for beanType := range f.conflicts {
		types = append(types, beanType.String())
	}
	sort.Strings(types)
	return fmt.Errorf("%w: concrete bean types %s", ErrBeanDuplicate, strings.Join(types, ", "))
}

// GetByType 使用泛型获取 Bean
func GetByType[T any]() T {
	return Resolve[T](GetInjector())
}

// GetByType 快捷别名 (BeanFactory 实例版本)
func (f *BeanFactory) GetByType(t reflect.Type) any {
	return f.Get(t)
}

// Apply 执行依赖注入 (优先使用静态注入，回退到反射)
func (f *BeanFactory) Apply(obj any) {
	v := reflect.ValueOf(obj)
	if v.Kind() == reflect.Ptr {
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return
	}

	// 1. 尝试使用静态注入 (阶段 62)
	structName := v.Type().Name()
	staticMu.RLock()
	runtimeInjector, runtimeOK := runtimeStaticInjectors[structName]
	staticInjector, staticOK := staticInjectors[structName]
	staticMu.RUnlock()
	if runtimeOK {
		runtimeInjector(f, obj)
		return
	}
	// Legacy generated injectors resolve through the process-wide facade. They
	// are safe only when this factory is the facade's current owner; isolated
	// runtimes fall back to reflection against their own container.
	facade := loadDefaultFacade()
	if staticOK && facade != nil && facade.injector == f {
		staticInjector(obj)
		return
	}

	// 2. 回退到反射注入
	for i := 0; i < v.NumField(); i++ {
		field := v.Type().Field(i)
		fieldValue := v.Field(i)

		// 优先处理 @Value 配置注入
		if fieldValue.Type() == reflect.TypeOf((*Value)(nil)) {
			if valueTag, ok := field.Tag.Lookup("value"); ok && valueTag != "" {
				f.injectValue(fieldValue, valueTag)
				continue
			}
		}

		// 处理 inject 标签
		if tag, ok := field.Tag.Lookup("inject"); ok {
			fieldType := field.Type
			// 如果标签是 "-"，则按类型自动注入
			if tag == "-" || tag == "" {
				bean := f.Get(fieldType)
				if bean != nil {
					f := v.Field(i)
					if f.CanSet() {
						f.Set(reflect.ValueOf(bean))
					} else {
						// 严苛模式：注入失败直接 Panic，防止运行时 nil 错误
						panic("IOC_INJECTION_FAILED: field must be exported (start with upper case) to be injected: " + v.Type().String() + "." + field.Name)
					}
				}
			}
		}
	}
}

// ApplyE injects dependencies using strict container-local resolution only.
func (f *BeanFactory) ApplyE(obj any) error {
	v := reflect.ValueOf(obj)
	if !v.IsValid() || v.Kind() != reflect.Ptr || v.IsNil() || v.Elem().Kind() != reflect.Struct {
		return fmt.Errorf("strict injection requires a non-nil pointer to struct, got %T", obj)
	}

	structType := v.Elem().Type()
	key := runtimeStaticInjectorKey(structType)
	staticMu.RLock()
	injector, ok := runtimeStaticInjectorsE[key]
	staticMu.RUnlock()
	if ok {
		if err := injector(f, obj); err != nil {
			return fmt.Errorf("strict static injection for %s: %w", structType, err)
		}
		return nil
	}
	return f.applyEReflect(v.Elem())
}

func (f *BeanFactory) applyEReflect(value reflect.Value) error {
	valueType := value.Type()
	valuePointerType := reflect.TypeFor[*Value]()
	for i := 0; i < value.NumField(); i++ {
		field := valueType.Field(i)
		fieldValue := value.Field(i)
		injectTag, inject := field.Tag.Lookup("inject")
		valueTag, valueInjection := field.Tag.Lookup("value")
		if !inject && !valueInjection {
			continue
		}
		if field.PkgPath != "" || !fieldValue.CanSet() {
			return fmt.Errorf("strict injection cannot set unexported field %s.%s (%s)", valueType, field.Name, field.Type)
		}
		if valueInjection {
			if field.Type != valuePointerType {
				return fmt.Errorf("strict Value injection field %s.%s must have type *bear.Value, got %s", valueType, field.Name, field.Type)
			}
			injectedValue, err := f.valueE(valueType, field.Name, valueTag)
			if err != nil {
				return err
			}
			fieldValue.Set(reflect.ValueOf(injectedValue))
			continue
		}
		_ = injectTag // Strict injection resolves every explicit inject field by its field type.
		bean, err := f.resolveE(field.Type)
		if err != nil {
			return fmt.Errorf("strict injection field %s.%s (%s): %w", valueType, field.Name, field.Type, err)
		}
		beanValue := reflect.ValueOf(bean)
		if !beanValue.IsValid() || !beanValue.Type().AssignableTo(field.Type) {
			return fmt.Errorf("%w: strict injection field %s.%s (%s)", ErrBeanMissing, valueType, field.Name, field.Type)
		}
		fieldValue.Set(beanValue)
	}
	return nil
}

func (f *BeanFactory) valueE(owner reflect.Type, fieldName, valueTag string) (*Value, error) {
	if valueTag == "" {
		return nil, fmt.Errorf("strict Value injection field %s.%s has an empty value tag", owner, fieldName)
	}
	prefix, key := valueParts(valueTag)
	fullKey := key
	if prefix != "" {
		fullKey = prefix + "." + key
	}
	f.mu.RLock()
	config, ok := f.beans[reflect.TypeFor[*SysConfig]()]
	f.mu.RUnlock()
	if !ok || config == nil {
		return nil, fmt.Errorf("%w: configuration value %s for field %s.%s", ErrBeanMissing, fullKey, owner, fieldName)
	}
	sysConfig, ok := config.(*SysConfig)
	if !ok || sysConfig == nil || sysConfig.Config == nil {
		return nil, fmt.Errorf("strict Value injection cannot read configuration for field %s.%s", owner, fieldName)
	}
	stored, ok := sysConfig.Config[fullKey]
	if !ok {
		return nil, fmt.Errorf("%w: configuration value %s for field %s.%s", ErrBeanMissing, fullKey, owner, fieldName)
	}
	return &Value{prefix: prefix, key: key, value: stored}, nil
}

func valueParts(valueTag string) (prefix, key string) {
	parts := strings.Split(valueTag, ".")
	if len(parts) == 1 {
		return "", valueTag
	}
	return parts[0], strings.Join(parts[1:], ".")
}

// injectValue 处理 @Value 配置注入
func (f *BeanFactory) injectValue(fieldValue reflect.Value, valueTag string) {
	// 解析 prefix 和 key
	var prefix, key string
	parts := strings.Split(valueTag, ".")
	if len(parts) == 1 {
		key = valueTag
	} else if len(parts) > 1 {
		prefix = parts[0]
		key = strings.Join(parts[1:], ".")
	}

	// 构建完整 key
	fullKey := key
	if prefix != "" {
		fullKey = prefix + "." + key
	}

	// 从 SysConfig 中获取值
	f.mu.RLock()
	config := f.beans[reflect.TypeOf((*SysConfig)(nil)).Elem()]
	f.mu.RUnlock()

	if config == nil {
		return
	}

	sysConfig, ok := config.(*SysConfig)
	if !ok || sysConfig == nil || sysConfig.Config == nil {
		return
	}

	// 从 Config map 中获取值
	if val, ok := sysConfig.Config[fullKey]; ok {
		value := &Value{
			prefix: prefix,
			key:    key,
			value:  val,
		}
		fieldValue.Set(reflect.ValueOf(value))
	}
}

// StaticApply 是为了未来代码生成准备的，它允许显式地注入字段而不需要反射
// 虽然目前仍然在 bear.go 中使用 Apply，但代码生成器会生成直接赋值的代码
func (f *BeanFactory) StaticApply(obj any, fieldName string, bean any) {
	v := reflect.ValueOf(obj)
	if v.Kind() == reflect.Ptr {
		v = v.Elem()
	}
	field := v.FieldByName(fieldName)
	if field.IsValid() && field.CanSet() {
		field.Set(reflect.ValueOf(bean))
	}
}

// GetBeanMapper 获取所有的 Bean 映射
func (f *BeanFactory) GetBeanMapper() map[reflect.Type]reflect.Value {
	f.mu.RLock()
	defer f.mu.RUnlock()

	res := make(map[reflect.Type]reflect.Value)
	for k, v := range f.beans {
		res[k] = reflect.ValueOf(v)
	}
	return res
}

func (f *BeanFactory) orderedBeans() []any {
	if f == nil {
		return nil
	}
	f.mu.RLock()
	defer f.mu.RUnlock()
	beans := make([]any, 0, len(f.order))
	for _, registeredType := range f.order {
		if bean, ok := f.beans[registeredType]; ok {
			duplicate := false
			for _, existing := range beans {
				if sameBeanInstance(existing, bean) {
					duplicate = true
					break
				}
			}
			if !duplicate {
				beans = append(beans, bean)
			}
		}
	}
	return beans
}
