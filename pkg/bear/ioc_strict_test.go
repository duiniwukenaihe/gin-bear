package bear

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/duiniwukenaihe/gin-bear/pkg/bear/testfixtures/staticinjectora"
	"github.com/duiniwukenaihe/gin-bear/pkg/bear/testfixtures/staticinjectorb"
)

type strictService interface {
	strictServiceMethod()
}

type strictServiceOne struct{}

func (*strictServiceOne) strictServiceMethod() {}

type strictServiceTwo struct{}

func (*strictServiceTwo) strictServiceMethod() {}

type strictMissingTarget struct {
	Service strictService `inject:"-"`
}

type strictPrivateTarget struct {
	service strictService `inject:"-"`
}

type strictValueTarget struct {
	Value *Value `value:"service.token"`
}

type strictInvalidValueTarget struct {
	Value string `value:"service.token"`
}

type strictDuplicateBean struct {
	name string
}

func (b *strictDuplicateBean) Name() string { return b.name }

type strictModule struct {
	name  string
	beans []Bean
}

func (m *strictModule) Name() string  { return m.name }
func (m *strictModule) Beans() []Bean { return m.beans }
func (*strictModule) Build(*Bear)     {}

type strictController struct {
	name string
}

func (c *strictController) Name() string { return c.name }
func (*strictController) Build(*Bear)    {}

type strictBatchOtherBean struct {
	name string
}

func (b *strictBatchOtherBean) Name() string { return b.name }

type strictAlias interface {
	strictAliasMethod()
}

type strictAliasBean struct {
	initCalls int
}

func (*strictAliasBean) Name() string                 { return "strict-alias" }
func (*strictAliasBean) strictAliasMethod()           {}
func (b *strictAliasBean) Init(context.Context) error { b.initCalls++; return nil }

type strictLifecycleClosingBean struct {
	onName func()
	once   sync.Once
}

func (b *strictLifecycleClosingBean) Name() string {
	b.once.Do(b.onName)
	return "lifecycle-closing"
}

type strictFactorySnapshot struct {
	beans     map[reflect.Type]any
	order     []reflect.Type
	concrete  map[reflect.Type]any
	conflicts map[reflect.Type]struct{}
}

func TestResolveEReportsMissingAndAmbiguousDependencies(t *testing.T) {
	t.Run("missing", func(t *testing.T) {
		_, err := ResolveE[strictService](NewBeanFactory())
		if !errors.Is(err, ErrBeanMissing) {
			t.Fatalf("ResolveE() error = %v, want ErrBeanMissing", err)
		}
		if !strings.Contains(err.Error(), reflect.TypeFor[strictService]().String()) {
			t.Fatalf("ResolveE() error = %v, want requested type", err)
		}
	})

	t.Run("ambiguous implicit implementations", func(t *testing.T) {
		factory := NewBeanFactory()
		factory.Set(&strictServiceOne{})
		factory.Set(&strictServiceTwo{})

		_, err := ResolveE[strictService](factory)
		if !errors.Is(err, ErrBeanAmbiguous) {
			t.Fatalf("ResolveE() error = %v, want ErrBeanAmbiguous", err)
		}
	})
}

func TestResolveEUsesExplicitInterfaceBindingBeforeImplicitCandidates(t *testing.T) {
	factory := NewBeanFactory()
	implicit := &strictServiceOne{}
	explicit := &strictServiceTwo{}
	factory.Set(implicit)
	if err := factory.TrySetWithInterface((*strictService)(nil), explicit); err != nil {
		t.Fatalf("TrySetWithInterface() error = %v", err)
	}

	got, err := ResolveE[strictService](factory)
	if err != nil {
		t.Fatalf("ResolveE() error = %v", err)
	}
	if got != explicit {
		t.Fatalf("ResolveE() = %T, want explicit %T", got, explicit)
	}
}

func TestStrictIOCRejectsDifferentConcreteInstancesAndAllowsIdempotence(t *testing.T) {
	factory := NewBeanFactory()
	factory.strict = true
	first := &strictDuplicateBean{name: "first"}
	second := &strictDuplicateBean{name: "second"}

	if err := factory.TrySet(first); err != nil {
		t.Fatalf("first TrySet() error = %v", err)
	}
	if err := factory.TrySet(first); err != nil {
		t.Fatalf("idempotent TrySet() error = %v", err)
	}
	if err := factory.TrySet(second); !errors.Is(err, ErrBeanDuplicate) {
		t.Fatalf("second TrySet() error = %v, want ErrBeanDuplicate", err)
	}
	if got := Resolve[*strictDuplicateBean](factory); got != first {
		t.Fatalf("resolved bean = %p, want first instance %p", got, first)
	}
}

func TestStrictIOCBeansERejectsDuplicateWithDefaultConfig(t *testing.T) {
	app := Ignite(NewSysConfig())
	first := &strictDuplicateBean{name: "default-first"}
	second := &strictDuplicateBean{name: "default-second"}
	if err := app.BeansE(first); err != nil {
		t.Fatalf("BeansE(first) error = %v", err)
	}
	if err := app.BeansE(second); !errors.Is(err, ErrBeanDuplicate) {
		t.Fatalf("BeansE(second) error = %v, want ErrBeanDuplicate", err)
	}
	if got := Resolve[*strictDuplicateBean](app.Runtime().Container); got != first {
		t.Fatalf("resolved bean = %p, want first %p", got, first)
	}
}

func TestStrictIOCBeansEBatchIsAtomic(t *testing.T) {
	app := Ignite(NewSysConfig())
	before := snapshotStrictFactory(app.Runtime().Container)
	beforeComponents := len(app.Runtime().Lifecycle.components)
	beforeEntries := len(app.Runtime().Lifecycle.beanEntries)

	err := app.BeansE(
		&strictDuplicateBean{name: "batch-first"},
		&strictDuplicateBean{name: "batch-second"},
	)
	if !errors.Is(err, ErrBeanDuplicate) {
		t.Fatalf("BeansE() error = %v, want ErrBeanDuplicate", err)
	}
	assertStrictFactorySnapshot(t, app.Runtime().Container, before)
	if len(app.Runtime().Lifecycle.components) != beforeComponents || len(app.Runtime().Lifecycle.beanEntries) != beforeEntries {
		t.Fatalf("failed batch changed lifecycle registrations: components=%d entries=%d", len(app.Runtime().Lifecycle.components), len(app.Runtime().Lifecycle.beanEntries))
	}
	if _, ok := app.exprData["batch-first"]; ok {
		t.Fatal("failed batch published first bean metadata")
	}
	if _, ok := app.exprData["batch-second"]; ok {
		t.Fatal("failed batch published second bean metadata")
	}
}

func TestStrictIOCAddModuleEBatchIsAtomic(t *testing.T) {
	app := Ignite(NewSysConfig())
	before := snapshotStrictFactory(app.Runtime().Container)
	beforeComponents := len(app.Runtime().Lifecycle.components)
	modules := []Module{
		&strictModule{name: "atomic-module-one", beans: []Bean{&strictBatchOtherBean{name: "module-unique"}}},
		&strictModule{name: "atomic-module-two", beans: []Bean{
			&strictDuplicateBean{name: "module-duplicate-one"},
			&strictDuplicateBean{name: "module-duplicate-two"},
		}},
	}

	err := app.AddModuleE(modules...)
	if !errors.Is(err, ErrBeanDuplicate) {
		t.Fatalf("AddModuleE() error = %v, want ErrBeanDuplicate", err)
	}
	assertStrictFactorySnapshot(t, app.Runtime().Container, before)
	if len(app.Runtime().Lifecycle.components) != beforeComponents {
		t.Fatalf("failed module batch changed lifecycle components: %d", len(app.Runtime().Lifecycle.components))
	}
	if len(app.modules) != 0 {
		t.Fatalf("failed module batch published metadata: %#v", app.modules)
	}
	for _, name := range []string{"module-unique", "module-duplicate-one", "module-duplicate-two"} {
		if _, ok := app.exprData[name]; ok {
			t.Fatalf("failed module batch published bean metadata %q", name)
		}
	}
}

func TestStrictIOCMountEBatchIsAtomic(t *testing.T) {
	app := Ignite(NewSysConfig())
	before := snapshotStrictFactory(app.Runtime().Container)
	beforeComponents := len(app.Runtime().Lifecycle.components)

	err := app.MountE("/atomic",
		&strictController{name: "controller-one"},
		&strictController{name: "controller-two"},
	)
	if !errors.Is(err, ErrBeanDuplicate) {
		t.Fatalf("MountE() error = %v, want ErrBeanDuplicate", err)
	}
	assertStrictFactorySnapshot(t, app.Runtime().Container, before)
	if len(app.Runtime().Lifecycle.components) != beforeComponents {
		t.Fatalf("failed mount batch changed lifecycle components: %d", len(app.Runtime().Lifecycle.components))
	}
	if len(app.mounts) != 0 {
		t.Fatalf("failed mount batch published metadata: %#v", app.mounts)
	}
	for _, name := range []string{"controller-one", "controller-two"} {
		if _, ok := app.exprData[name]; ok {
			t.Fatalf("failed mount batch published bean metadata %q", name)
		}
	}
}

func TestStrictIOCRemoveRebuildsConcreteAndConflictIndexes(t *testing.T) {
	t.Run("removed concrete permits replacement", func(t *testing.T) {
		config := NewSysConfig()
		config.SetFrameworkStrict(true)
		app := Ignite(config)
		app.Beans(&strictDuplicateBean{name: "legacy-first"})
		app.Beans(&strictDuplicateBean{name: "legacy-second"})
		app.Runtime().Container.Remove(reflect.TypeFor[*strictDuplicateBean]())

		replacement := &strictDuplicateBean{name: "strict-replacement"}
		if err := app.BeansE(replacement); err != nil {
			t.Fatalf("BeansE(replacement) error = %v", err)
		}
		if err := app.Runtime().Container.strictConflictError(); err != nil {
			t.Fatalf("strictConflictError() after replacement = %v", err)
		}
	})

	t.Run("interface alias keeps concrete occupied", func(t *testing.T) {
		config := NewSysConfig()
		config.SetFrameworkStrict(true)
		app := Ignite(config)
		first := &strictDuplicateBean{name: "aliased-first"}
		if err := app.BeansE(first); err != nil {
			t.Fatal(err)
		}
		if err := app.Runtime().Container.TrySetWithInterface((*Bean)(nil), first); err != nil {
			t.Fatal(err)
		}
		app.Runtime().Container.Remove(reflect.TypeFor[*strictDuplicateBean]())

		err := app.BeansE(&strictDuplicateBean{name: "aliased-second"})
		if !errors.Is(err, ErrBeanDuplicate) {
			t.Fatalf("BeansE(second) error = %v, want ErrBeanDuplicate", err)
		}
	})
}

func TestStrictIOCAliasAppliesAndInitializesOnce(t *testing.T) {
	config := NewSysConfig()
	config.SetFrameworkStrict(true)
	app := Ignite(config)
	bean := &strictAliasBean{}
	if err := app.BeansE(bean); err != nil {
		t.Fatal(err)
	}
	if err := app.Runtime().Container.TrySetWithInterface((*strictAlias)(nil), bean); err != nil {
		t.Fatal(err)
	}
	applyCalls := 0
	registerRuntimeStaticInjectorEForTest(t, runtimeStaticInjectorKey(reflect.TypeFor[strictAliasBean]()), func(_ *BeanFactory, obj any) error {
		if obj != bean {
			t.Fatalf("ApplyE object = %p, want %p", obj, bean)
		}
		applyCalls++
		return nil
	})

	if err := app.ApplyAll(context.Background()); err != nil {
		t.Fatalf("ApplyAll() error = %v", err)
	}
	if applyCalls != 1 || bean.initCalls != 1 {
		t.Fatalf("alias calls: ApplyE=%d Init=%d, want 1/1", applyCalls, bean.initCalls)
	}
}

func TestStrictIOCEAPIsRejectTypedNilWithoutPanic(t *testing.T) {
	app := Ignite(NewSysConfig())
	var bean *strictDuplicateBean
	var module *strictModule
	var controller *strictController

	assertStrictErrorWithoutPanic(t, "BeansE", func() error { return app.BeansE(bean) })
	assertStrictErrorWithoutPanic(t, "AddModuleE", func() error { return app.AddModuleE(module) })
	assertStrictErrorWithoutPanic(t, "MountE", func() error { return app.MountE("/nil", controller) })
	assertStrictErrorWithoutPanic(t, "module bean", func() error {
		return app.AddModuleE(&strictModule{name: "typed-nil-bean", beans: []Bean{bean}})
	})
	if len(app.modules) != 0 || len(app.mounts) != 0 {
		t.Fatalf("typed nil published metadata: modules=%d mounts=%d", len(app.modules), len(app.mounts))
	}
}

func TestStrictIOCBeansEBatchRollsBackWhenLifecycleClosesBeforeCommit(t *testing.T) {
	app := Ignite(NewSysConfig())
	before := snapshotStrictFactory(app.Runtime().Container)
	beforeComponents := len(app.Runtime().Lifecycle.components)
	beforeEntries := len(app.Runtime().Lifecycle.beanEntries)
	var startErr error
	closing := &strictLifecycleClosingBean{onName: func() {
		startErr = app.Runtime().Lifecycle.Start(context.Background())
	}}

	err := app.BeansE(closing, &strictBatchOtherBean{name: "after-close"})
	if !errors.Is(err, ErrLifecycleRegistrationClosed) {
		t.Fatalf("BeansE() error = %v, want ErrLifecycleRegistrationClosed", err)
	}
	if startErr != nil {
		t.Fatalf("Lifecycle.Start() error = %v", startErr)
	}
	assertStrictFactorySnapshot(t, app.Runtime().Container, before)
	if len(app.Runtime().Lifecycle.components) != beforeComponents || len(app.Runtime().Lifecycle.beanEntries) != beforeEntries {
		t.Fatalf("closed batch changed lifecycle registrations: components=%d entries=%d", len(app.Runtime().Lifecycle.components), len(app.Runtime().Lifecycle.beanEntries))
	}
	if _, ok := app.exprData["lifecycle-closing"]; ok {
		t.Fatal("closed batch published first bean metadata")
	}
	if _, ok := app.exprData["after-close"]; ok {
		t.Fatal("closed batch published second bean metadata")
	}
	if err := app.Runtime().Lifecycle.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestStrictIOCBeansEBatchRejectsClosedLifecycleWithoutMutation(t *testing.T) {
	app := Ignite(NewSysConfig())
	if err := app.ApplyAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	before := snapshotStrictFactory(app.Runtime().Container)
	beforeComponents := len(app.Runtime().Lifecycle.components)

	err := app.BeansE(
		&strictDuplicateBean{name: "closed-first"},
		&strictBatchOtherBean{name: "closed-second"},
	)
	if !errors.Is(err, ErrLifecycleRegistrationClosed) {
		t.Fatalf("BeansE() error = %v, want ErrLifecycleRegistrationClosed", err)
	}
	assertStrictFactorySnapshot(t, app.Runtime().Container, before)
	if len(app.Runtime().Lifecycle.components) != beforeComponents {
		t.Fatalf("closed lifecycle batch changed components: %d", len(app.Runtime().Lifecycle.components))
	}
}

func TestApplyEReportsInjectionDiagnostics(t *testing.T) {
	t.Run("not pointer to struct", func(t *testing.T) {
		err := NewBeanFactory().ApplyE(strictMissingTarget{})
		if err == nil || !strings.Contains(err.Error(), "pointer to struct") {
			t.Fatalf("ApplyE() error = %v, want pointer diagnostic", err)
		}
	})

	t.Run("missing dependency", func(t *testing.T) {
		err := NewBeanFactory().ApplyE(&strictMissingTarget{})
		if !errors.Is(err, ErrBeanMissing) || !strings.Contains(err.Error(), "Service") {
			t.Fatalf("ApplyE() error = %v, want missing field diagnostic", err)
		}
	})

	t.Run("unexported inject field", func(t *testing.T) {
		err := NewBeanFactory().ApplyE(&strictPrivateTarget{})
		if err == nil || !strings.Contains(err.Error(), "service") {
			t.Fatalf("ApplyE() error = %v, want unexported field diagnostic", err)
		}
	})

	t.Run("missing configuration value", func(t *testing.T) {
		factory := NewBeanFactory()
		factory.Set(&SysConfig{Config: UserConfig{}})
		err := factory.ApplyE(&strictValueTarget{})
		if !errors.Is(err, ErrBeanMissing) || !strings.Contains(err.Error(), "service.token") {
			t.Fatalf("ApplyE() error = %v, want missing value diagnostic", err)
		}
	})

	t.Run("invalid configuration value field type", func(t *testing.T) {
		factory := NewBeanFactory()
		factory.Set(&SysConfig{Config: UserConfig{"service.token": "value"}})
		err := factory.ApplyE(&strictInvalidValueTarget{})
		if err == nil || !strings.Contains(err.Error(), "*bear.Value") {
			t.Fatalf("ApplyE() error = %v, want Value type diagnostic", err)
		}
	})
}

func TestStaticInjectorKeyUsesFullPackagePath(t *testing.T) {
	first := &staticinjectora.Target{}
	second := &staticinjectorb.Target{}
	firstKey := runtimeStaticInjectorKey(reflect.TypeOf(*first))
	secondKey := runtimeStaticInjectorKey(reflect.TypeOf(*second))
	if firstKey == secondKey {
		t.Fatalf("static injector keys collide: %q", firstKey)
	}

	registerRuntimeStaticInjectorEForTest(t, firstKey, func(_ *BeanFactory, obj any) error {
		obj.(*staticinjectora.Target).Marker = "first"
		return nil
	})
	registerRuntimeStaticInjectorEForTest(t, secondKey, func(_ *BeanFactory, obj any) error {
		obj.(*staticinjectorb.Target).Marker = "second"
		return nil
	})

	factory := NewBeanFactory()
	if err := factory.ApplyE(first); err != nil {
		t.Fatalf("ApplyE(first) error = %v", err)
	}
	if err := factory.ApplyE(second); err != nil {
		t.Fatalf("ApplyE(second) error = %v", err)
	}
	if first.Marker != "first" || second.Marker != "second" {
		t.Fatalf("static injection markers = %q/%q", first.Marker, second.Marker)
	}
}

func TestStrictIOCApplyAllRejectsLegacyRegistrationConflict(t *testing.T) {
	config := NewSysConfig()
	config.SetFrameworkStrict(true)
	app := Ignite(config)
	app.Beans(&strictDuplicateBean{name: "first"})
	app.Beans(&strictDuplicateBean{name: "second"})

	err := app.ApplyAll(context.Background())
	if !errors.Is(err, ErrBeanDuplicate) {
		t.Fatalf("ApplyAll() error = %v, want ErrBeanDuplicate", err)
	}
}

func TestStrictIOCErrorReturningBearRegistrationDoesNotPublishFailedMetadata(t *testing.T) {
	config := NewSysConfig()
	config.SetFrameworkStrict(true)
	app := Ignite(config)
	first := &strictDuplicateBean{name: "first"}
	if err := app.BeansE(first); err != nil {
		t.Fatalf("BeansE(first) error = %v", err)
	}
	if err := app.BeansE(&strictDuplicateBean{name: "second"}); !errors.Is(err, ErrBeanDuplicate) {
		t.Fatalf("BeansE(second) error = %v, want ErrBeanDuplicate", err)
	}
	if _, ok := app.exprData["second"]; ok {
		t.Fatal("BeansE published metadata for rejected bean")
	}

	module := &strictModule{name: "rejected-module", beans: []Bean{&strictDuplicateBean{name: "third"}}}
	if err := app.AddModuleE(module); !errors.Is(err, ErrBeanDuplicate) {
		t.Fatalf("AddModuleE() error = %v, want ErrBeanDuplicate", err)
	}
	if len(app.modules) != 0 {
		t.Fatalf("AddModuleE published module metadata: %#v", app.modules)
	}

	if err := app.MountE("/strict", &strictController{name: "accepted-controller"}); err != nil {
		t.Fatalf("MountE(accepted) error = %v", err)
	}
	if err := app.MountE("/strict", &strictController{name: "rejected-controller"}); !errors.Is(err, ErrBeanDuplicate) {
		t.Fatalf("MountE() error = %v, want ErrBeanDuplicate", err)
	}
	if len(app.mounts) != 1 {
		t.Fatalf("MountE published mount metadata: %#v", app.mounts)
	}
}

func registerRuntimeStaticInjectorEForTest(t *testing.T, key string, injector RuntimeStaticInjectorE) {
	t.Helper()
	staticMu.Lock()
	previous, hadPrevious := runtimeStaticInjectorsE[key]
	staticMu.Unlock()
	t.Cleanup(func() {
		staticMu.Lock()
		defer staticMu.Unlock()
		if hadPrevious {
			runtimeStaticInjectorsE[key] = previous
		} else {
			delete(runtimeStaticInjectorsE, key)
		}
	})
	RegisterRuntimeStaticInjectorE(key, injector)
}

func snapshotStrictFactory(factory *BeanFactory) strictFactorySnapshot {
	factory.mu.RLock()
	defer factory.mu.RUnlock()
	beans := make(map[reflect.Type]any, len(factory.beans))
	for beanType, bean := range factory.beans {
		beans[beanType] = bean
	}
	concrete := make(map[reflect.Type]any, len(factory.concrete))
	for beanType, bean := range factory.concrete {
		concrete[beanType] = bean
	}
	conflicts := make(map[reflect.Type]struct{}, len(factory.conflicts))
	for beanType := range factory.conflicts {
		conflicts[beanType] = struct{}{}
	}
	return strictFactorySnapshot{
		beans:     beans,
		order:     append([]reflect.Type(nil), factory.order...),
		concrete:  concrete,
		conflicts: conflicts,
	}
}

func assertStrictFactorySnapshot(t *testing.T, factory *BeanFactory, want strictFactorySnapshot) {
	t.Helper()
	got := snapshotStrictFactory(factory)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("factory changed after failed batch:\ngot:  %#v\nwant: %#v", got, want)
	}
}

func assertStrictErrorWithoutPanic(t *testing.T, name string, call func() error) {
	t.Helper()
	defer func() {
		if recovered := recover(); recovered != nil {
			t.Fatalf("%s panicked for typed nil: %v", name, recovered)
		}
	}()
	if err := call(); err == nil || !strings.Contains(err.Error(), "nil") {
		t.Fatalf("%s error = %v, want typed nil diagnostic", name, err)
	}
}
