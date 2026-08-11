package bear

import (
	"context"
	"errors"
	"reflect"
	"strings"
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
