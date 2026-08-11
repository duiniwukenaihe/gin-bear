package bear

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

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

type strictReentrantBean struct {
	name   string
	onName func()
	once   sync.Once
}

func (b *strictReentrantBean) Name() string {
	b.once.Do(b.onName)
	return b.name
}

type strictReentrantModule struct {
	name    string
	beans   []Bean
	onBeans func()
	once    sync.Once
}

func (m *strictReentrantModule) Name() string { return m.name }
func (m *strictReentrantModule) Beans() []Bean {
	m.once.Do(m.onBeans)
	return m.beans
}
func (*strictReentrantModule) Build(*Bear) {}

type strictConcurrentBean[T any] struct {
	name string
}

func (b *strictConcurrentBean[T]) Name() string { return b.name }

type strictConcurrentController[T any] struct {
	name string
}

func (c *strictConcurrentController[T]) Name() string { return c.name }
func (*strictConcurrentController[T]) Build(*Bear)    {}

type strictConcurrentTag01 struct{}
type strictConcurrentTag02 struct{}
type strictConcurrentTag03 struct{}
type strictConcurrentTag04 struct{}
type strictConcurrentTag05 struct{}
type strictConcurrentTag06 struct{}
type strictConcurrentTag07 struct{}
type strictConcurrentTag08 struct{}
type strictConcurrentTag09 struct{}
type strictConcurrentTag10 struct{}
type strictConcurrentTag11 struct{}
type strictConcurrentTag12 struct{}
type strictConcurrentTag13 struct{}
type strictConcurrentTag14 struct{}
type strictConcurrentTag15 struct{}
type strictConcurrentTag16 struct{}
type strictConcurrentTag17 struct{}
type strictConcurrentTag18 struct{}

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

func TestStrictIOCConcurrentERegistration(t *testing.T) {
	app := Ignite(NewSysConfig())
	beforeBeans := len(app.Runtime().Container.beans)
	beforeOrder := len(app.Runtime().Container.order)
	beforeConcrete := len(app.Runtime().Container.concrete)
	beforeMetadata := len(app.exprData)

	operations := []func() error{
		func() error {
			return app.BeansE(
				&strictConcurrentBean[strictConcurrentTag01]{name: "concurrent-bean-01"},
				&strictConcurrentBean[strictConcurrentTag02]{name: "concurrent-bean-02"},
				&strictConcurrentBean[strictConcurrentTag03]{name: "concurrent-bean-03"},
			)
		},
		func() error {
			return app.BeansE(
				&strictConcurrentBean[strictConcurrentTag04]{name: "concurrent-bean-04"},
				&strictConcurrentBean[strictConcurrentTag05]{name: "concurrent-bean-05"},
				&strictConcurrentBean[strictConcurrentTag06]{name: "concurrent-bean-06"},
			)
		},
		func() error {
			return app.AddModuleE(
				&strictModule{name: "concurrent-module-01", beans: []Bean{&strictConcurrentBean[strictConcurrentTag07]{name: "concurrent-bean-07"}}},
				&strictModule{name: "concurrent-module-02", beans: []Bean{&strictConcurrentBean[strictConcurrentTag08]{name: "concurrent-bean-08"}}},
				&strictModule{name: "concurrent-module-03", beans: []Bean{&strictConcurrentBean[strictConcurrentTag09]{name: "concurrent-bean-09"}}},
			)
		},
		func() error {
			return app.AddModuleE(
				&strictModule{name: "concurrent-module-04", beans: []Bean{&strictConcurrentBean[strictConcurrentTag10]{name: "concurrent-bean-10"}}},
				&strictModule{name: "concurrent-module-05", beans: []Bean{&strictConcurrentBean[strictConcurrentTag11]{name: "concurrent-bean-11"}}},
				&strictModule{name: "concurrent-module-06", beans: []Bean{&strictConcurrentBean[strictConcurrentTag12]{name: "concurrent-bean-12"}}},
			)
		},
		func() error {
			return app.MountE("/concurrent-a",
				&strictConcurrentController[strictConcurrentTag13]{name: "concurrent-controller-13"},
				&strictConcurrentController[strictConcurrentTag14]{name: "concurrent-controller-14"},
				&strictConcurrentController[strictConcurrentTag15]{name: "concurrent-controller-15"},
			)
		},
		func() error {
			return app.MountE("/concurrent-b",
				&strictConcurrentController[strictConcurrentTag16]{name: "concurrent-controller-16"},
				&strictConcurrentController[strictConcurrentTag17]{name: "concurrent-controller-17"},
				&strictConcurrentController[strictConcurrentTag18]{name: "concurrent-controller-18"},
			)
		},
	}

	start := make(chan struct{})
	errorsCh := make(chan error, len(operations))
	var wait sync.WaitGroup
	for _, operation := range operations {
		wait.Add(1)
		go func(register func() error) {
			defer wait.Done()
			<-start
			errorsCh <- register()
		}(operation)
	}
	close(start)
	wait.Wait()
	close(errorsCh)
	for err := range errorsCh {
		if err != nil {
			t.Fatalf("concurrent E registration error = %v", err)
		}
	}

	const registeredBeans = 18
	if got := len(app.Runtime().Container.beans); got != beforeBeans+registeredBeans {
		t.Fatalf("container beans = %d, want %d", got, beforeBeans+registeredBeans)
	}
	if got := len(app.Runtime().Container.order); got != beforeOrder+registeredBeans {
		t.Fatalf("container order = %d, want %d", got, beforeOrder+registeredBeans)
	}
	if got := len(app.Runtime().Container.concrete); got != beforeConcrete+registeredBeans {
		t.Fatalf("container concrete = %d, want %d", got, beforeConcrete+registeredBeans)
	}
	if got := len(app.exprData); got != beforeMetadata+registeredBeans {
		t.Fatalf("exprData size = %d, want %d", got, beforeMetadata+registeredBeans)
	}
	for index := 1; index <= 12; index++ {
		name := fmt.Sprintf("concurrent-bean-%02d", index)
		if _, ok := app.exprData[name]; !ok {
			t.Fatalf("exprData missing %q", name)
		}
	}
	for index := 13; index <= 18; index++ {
		name := fmt.Sprintf("concurrent-controller-%02d", index)
		if _, ok := app.exprData[name]; !ok {
			t.Fatalf("exprData missing %q", name)
		}
	}
	if got := len(app.modules); got != 6 {
		t.Fatalf("modules size = %d, want 6", got)
	}
	if got := len(app.mounts); got != 2 {
		t.Fatalf("mounts size = %d, want 2", got)
	}
	mounted := make(map[string]int, len(app.mounts))
	for _, mount := range app.mounts {
		mounted[mount.Group] = len(mount.Classes)
	}
	if mounted["/concurrent-a"] != 3 || mounted["/concurrent-b"] != 3 {
		t.Fatalf("mount metadata = %#v, want both groups with 3 classes", mounted)
	}
}

func TestStrictIOCUserCallbacksCanReenterERegistration(t *testing.T) {
	t.Run("bean name callback", func(t *testing.T) {
		app := Ignite(NewSysConfig())
		inner := &strictBatchOtherBean{name: "reentrant-name-inner"}
		outer := &strictReentrantBean{
			name: "reentrant-name-outer",
			onName: func() {
				if err := app.BeansE(inner); err != nil {
					t.Errorf("nested BeansE() error = %v", err)
				}
			},
		}

		done := make(chan error, 1)
		go func() { done <- app.BeansE(outer) }()
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("BeansE() error = %v", err)
			}
		case <-time.After(time.Second):
			t.Fatal("BeansE() deadlocked while Bean.Name re-entered strict registration")
		}
	})

	t.Run("module beans callback", func(t *testing.T) {
		app := Ignite(NewSysConfig())
		inner := &strictBatchOtherBean{name: "reentrant-module-inner"}
		module := &strictReentrantModule{
			name: "reentrant-module",
			onBeans: func() {
				if err := app.BeansE(inner); err != nil {
					t.Errorf("nested BeansE() error = %v", err)
				}
			},
		}

		done := make(chan error, 1)
		go func() { done <- app.AddModuleE(module) }()
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("AddModuleE() error = %v", err)
			}
		case <-time.After(time.Second):
			t.Fatal("AddModuleE() deadlocked while Module.Beans re-entered strict registration")
		}
	})
}

func TestStrictIOCLateEmptyRegistrationChecksLifecycle(t *testing.T) {
	t.Run("BeansE empty batch", func(t *testing.T) {
		app := Ignite(NewSysConfig())
		if err := app.ApplyAll(context.Background()); err != nil {
			t.Fatal(err)
		}
		if err := app.BeansE(); !errors.Is(err, ErrLifecycleRegistrationClosed) {
			t.Fatalf("BeansE() error = %v, want ErrLifecycleRegistrationClosed", err)
		}
	})

	t.Run("AddModuleE module with empty beans", func(t *testing.T) {
		app := Ignite(NewSysConfig())
		if err := app.ApplyAll(context.Background()); err != nil {
			t.Fatal(err)
		}
		beforeMetadata := len(app.exprData)
		err := app.AddModuleE(&strictModule{name: "late-empty-module"})
		if !errors.Is(err, ErrLifecycleRegistrationClosed) {
			t.Fatalf("AddModuleE() error = %v, want ErrLifecycleRegistrationClosed", err)
		}
		if len(app.modules) != 0 || len(app.exprData) != beforeMetadata {
			t.Fatalf("late module published metadata: modules=%d exprData=%d", len(app.modules), len(app.exprData))
		}
	})

	t.Run("MountE empty classes", func(t *testing.T) {
		app := Ignite(NewSysConfig())
		if err := app.ApplyAll(context.Background()); err != nil {
			t.Fatal(err)
		}
		err := app.MountE("/late-empty")
		if !errors.Is(err, ErrLifecycleRegistrationClosed) {
			t.Fatalf("MountE() error = %v, want ErrLifecycleRegistrationClosed", err)
		}
		if len(app.mounts) != 0 {
			t.Fatalf("late empty mount published metadata: %#v", app.mounts)
		}
	})

	t.Run("MountE existing instance", func(t *testing.T) {
		app := Ignite(NewSysConfig())
		controller := &strictController{name: "existing-controller"}
		if err := app.BeansE(controller); err != nil {
			t.Fatal(err)
		}
		if err := app.ApplyAll(context.Background()); err != nil {
			t.Fatal(err)
		}
		beforeMetadata := len(app.exprData)
		controller.name = "late-existing-controller"
		err := app.MountE("/late-existing", controller)
		if !errors.Is(err, ErrLifecycleRegistrationClosed) {
			t.Fatalf("MountE() error = %v, want ErrLifecycleRegistrationClosed", err)
		}
		if len(app.mounts) != 0 || len(app.exprData) != beforeMetadata {
			t.Fatalf("late existing mount published metadata: mounts=%d exprData=%d", len(app.mounts), len(app.exprData))
		}
		if _, ok := app.exprData["late-existing-controller"]; ok {
			t.Fatal("late existing controller metadata was published")
		}
	})

	t.Run("standalone factory empty batch", func(t *testing.T) {
		if err := NewBeanFactory().trySetBatchStrict(nil); err != nil {
			t.Fatalf("trySetBatchStrict(nil) error = %v, want nil", err)
		}
	})
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
