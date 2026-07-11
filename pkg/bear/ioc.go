package bear

import (
	"reflect"
	"strings"
	"sync"
)

// BeanFactory 负责管理所有的 Bean
type BeanFactory struct {
	mu       sync.RWMutex
	beans    map[reflect.Type]any
	order    []reflect.Type
	onSet    func(reflect.Type, any)
	onRemove func(reflect.Type)
}

var bootstrapInjector = NewBeanFactory()

// StaticInjector 静态注入函数定义
type StaticInjector func(interface{})

var staticInjectors = make(map[string]StaticInjector)
var staticMu sync.RWMutex

// RegisterStaticInjector 注册静态注入器
func RegisterStaticInjector(name string, injector StaticInjector) {
	staticMu.Lock()
	defer staticMu.Unlock()
	staticInjectors[name] = injector
}

// GetInjector 获取单例注入器
func GetInjector() *BeanFactory {
	if facade := loadDefaultFacade(); facade != nil && facade.injector != nil {
		return facade.injector
	}
	setDefaultInjector(bootstrapInjector)
	return bootstrapInjector
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
	if f == nil || bean == nil {
		return
	}
	v := reflect.ValueOf(bean)
	f.mu.Lock()
	if _, exists := f.beans[v.Type()]; !exists {
		f.order = append(f.order, v.Type())
	}
	f.beans[v.Type()] = bean
	onSet := f.onSet
	f.mu.Unlock()
	if onSet != nil {
		onSet(v.Type(), bean)
	}
}

// SetWithInterface 注册一个 Bean 并绑定到指定接口类型
// ifacePtr 必须是指向接口的指针，例如 (*MyInterface)(nil)
func (f *BeanFactory) SetWithInterface(ifacePtr any, bean any) {
	t := reflect.TypeOf(ifacePtr).Elem()
	if t.Kind() != reflect.Interface {
		// 如果不是接口，尝试作为普通类型注册
		f.Set(bean)
		return
	}
	f.mu.Lock()
	if _, exists := f.beans[t]; !exists {
		f.order = append(f.order, t)
	}
	f.beans[t] = bean
	onSet := f.onSet
	f.mu.Unlock()
	if onSet != nil {
		onSet(t, bean)
	}
}

// Remove 移除一个 Bean
func (f *BeanFactory) Remove(t reflect.Type) {
	f.mu.Lock()
	delete(f.beans, t)
	for i, registeredType := range f.order {
		if registeredType == t {
			f.order = append(f.order[:i], f.order[i+1:]...)
			break
		}
	}
	onRemove := f.onRemove
	f.mu.Unlock()
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
	staticInjector, ok := staticInjectors[structName]
	staticMu.RUnlock()
	if ok {
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
