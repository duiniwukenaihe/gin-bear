package bear

import (
	"reflect"
	"testing"

	"github.com/duiniwukenaihe/gin-bear/pkg/bear/testfixtures/staticinjectora"
	"github.com/duiniwukenaihe/gin-bear/pkg/bear/testfixtures/staticinjectorb"
)

// TestRuntimeStaticInjectorsNamespaceSameNamedTypesAcrossPackages pins the
// registry key contract for generated injectors. The two fixture packages
// declare an identically named Target, so a bare struct name cannot address
// them independently: it would silently route both to whichever injector was
// registered last. Package-qualified keys must take precedence, and a bare-name
// registration must not shadow them.
func TestRuntimeStaticInjectorsNamespaceSameNamedTypesAcrossPackages(t *testing.T) {
	keyA := runtimeStaticInjectorKey(reflect.TypeFor[staticinjectora.Target]())
	keyB := runtimeStaticInjectorKey(reflect.TypeFor[staticinjectorb.Target]())
	if keyA == "" || keyB == "" || keyA == keyB {
		t.Fatalf("fixture injector keys are not distinct: %q and %q", keyA, keyB)
	}
	t.Cleanup(swapRuntimeStaticInjectors(keyA, keyB, "Target"))

	RegisterRuntimeStaticInjector(keyA, func(_ *BeanFactory, obj interface{}) {
		if target, ok := obj.(*staticinjectora.Target); ok {
			target.Marker = "package-a"
		}
	})
	RegisterRuntimeStaticInjector(keyB, func(_ *BeanFactory, obj interface{}) {
		if target, ok := obj.(*staticinjectorb.Target); ok {
			target.Marker = "package-b"
		}
	})
	// Stands in for an injector generated before package-qualified keys existed.
	// It must never win over a package-qualified entry.
	RegisterRuntimeStaticInjector("Target", func(_ *BeanFactory, obj interface{}) {
		switch target := obj.(type) {
		case *staticinjectora.Target:
			target.Marker = "bare-name"
		case *staticinjectorb.Target:
			target.Marker = "bare-name"
		}
	})

	factory := NewBeanFactory()

	targetA := &staticinjectora.Target{}
	factory.Apply(targetA)
	if targetA.Marker != "package-a" {
		t.Fatalf("staticinjectora.Target marker = %q, want %q", targetA.Marker, "package-a")
	}

	targetB := &staticinjectorb.Target{}
	factory.Apply(targetB)
	if targetB.Marker != "package-b" {
		t.Fatalf("staticinjectorb.Target marker = %q, want %q", targetB.Marker, "package-b")
	}
}

// TestFrameworkInjectorsSurviveBareNameShadowing pins the reason the framework
// registers its own legacy injectors under both keys. A generated injector for
// a user type that happens to share a framework type name used to overwrite the
// bare-name entry, and the generated injector's unchecked type assertion then
// panicked on the framework type.
func TestFrameworkInjectorsSurviveBareNameShadowing(t *testing.T) {
	key := runtimeStaticInjectorKey(reflect.TypeFor[AuthFairing]())
	if key == "" {
		t.Fatal("AuthFairing has no package-qualified injector key")
	}
	t.Cleanup(swapRuntimeStaticInjectors(key, "AuthFairing"))

	RegisterRuntimeStaticInjector("AuthFairing", func(_ *BeanFactory, obj interface{}) {
		target := obj.(*AuthFairing)
		target.JWTUtil = nil
		target.TokenManager = nil
	})

	factory := NewBeanFactory()
	jwtUtil := newJWTUtilFromAuthConfig(nil)
	factory.Set(jwtUtil)

	target := &AuthFairing{JWTUtil: jwtUtil}
	factory.Apply(target)

	if target.JWTUtil != jwtUtil {
		t.Fatalf("AuthFairing.JWTUtil = %v, want the registered JWT utility", target.JWTUtil)
	}
}

func swapRuntimeStaticInjectors(keys ...string) func() {
	staticMu.Lock()
	previous := make(map[string]RuntimeStaticInjector, len(keys))
	present := make(map[string]bool, len(keys))
	for _, key := range keys {
		injector, ok := runtimeStaticInjectors[key]
		previous[key] = injector
		present[key] = ok
	}
	staticMu.Unlock()

	return func() {
		staticMu.Lock()
		defer staticMu.Unlock()
		for _, key := range keys {
			if present[key] {
				runtimeStaticInjectors[key] = previous[key]
				continue
			}
			delete(runtimeStaticInjectors, key)
		}
	}
}
