package bear

import (
	"fmt"
	"reflect"
	"strings"
	"sync"
)

// BeanFactory 负责管理所有的 Bean
type BeanFactory struct {
	mu       sync.RWMutex
	beans    map[reflect.Type]any
	order    []reflect.Type
	onSet    func(reflect.Type, any, func()) error
	onRemove func(reflect.Type)
}

var bootstrapInjector = NewBeanFactory()

// StaticInjector 静态注入函数定义
type StaticInjector func(interface{})

// RuntimeStaticInjector resolves dependencies from the BeanFactory that owns
// the object being injected.
type RuntimeStaticInjector func(*BeanFactory, interface{})

var staticInjectors = make(map[string]StaticInjector)
var runtimeStaticInjectors = make(map[string]RuntimeStaticInjector)
var staticMu sync.RWMutex

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
	return &BeanFactory{beans: make(map[reflect.Type]any)}
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

// Set 注册一个 Bean
func (f *BeanFactory) Set(bean any) {
	_ = f.TrySet(bean)
}

// TrySet registers a bean or reports that the owning lifecycle is closed.
func (f *BeanFactory) TrySet(bean any) error {
	if f == nil || bean == nil {
		return nil
	}
	v := reflect.ValueOf(bean)
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.trySet(v.Type(), bean)
}

func (f *BeanFactory) trySet(beanType reflect.Type, bean any) error {
	commit := func() {
		if _, exists := f.beans[beanType]; !exists {
			f.order = append(f.order, beanType)
		}
		f.beans[beanType] = bean
	}
	if f.onSet != nil {
		return f.onSet(beanType, bean, commit)
	}
	commit()
	return nil
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
	return f.trySet(interfaceType, bean)
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
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.beans, t)
	for i, registeredType := range f.order {
		if registeredType == t {
			f.order = append(f.order[:i], f.order[i+1:]...)
			break
		}
	}
	onRemove := f.onRemove
	if onRemove != nil {
		onRemove(t)
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
			beans = append(beans, bean)
		}
	}
	return beans
}
