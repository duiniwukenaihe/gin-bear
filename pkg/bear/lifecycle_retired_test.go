package bear

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/alicebob/miniredis/v2"
)

type retiredLifecycleResource struct{ stops atomic.Int32 }

func (*retiredLifecycleResource) lifecyclePrestarted() bool               { return true }
func (r *retiredLifecycleResource) ShutdownContext(context.Context) error { r.stops.Add(1); return nil }

func TestLifecycleStopsRetiredPrestartedResources(t *testing.T) {
	for _, replace := range []bool{false, true} {
		name := "remove"
		if replace {
			name = "replace"
		}
		t.Run(name, func(t *testing.T) {
			lifecycle := newLifecycleWithMode(true)
			old, replacement := &retiredLifecycleResource{}, &retiredLifecycleResource{}
			typ := reflect.TypeOf(old)
			lifecycle.setBean(typ, old)
			if replace {
				lifecycle.setBean(typ, replacement)
			} else if err := lifecycle.removeBean(typ); err != nil {
				t.Fatal(err)
			}
			for range 2 {
				if err := lifecycle.Stop(context.Background()); err != nil {
					t.Fatal(err)
				}
			}
			if got := old.stops.Load(); got != 1 {
				t.Fatalf("retired resource stops = %d, want 1", got)
			}
			if replace && replacement.stops.Load() != 1 {
				t.Fatalf("replacement stops = %d, want 1", replacement.stops.Load())
			}
		})
	}
}

func TestLifecycleRollbackStopsRetiredPrestartedResource(t *testing.T) {
	lifecycle := newLifecycleWithMode(true)
	resource := &retiredLifecycleResource{}
	lifecycle.setBean(reflect.TypeOf(resource), resource)
	if err := lifecycle.removeBean(reflect.TypeOf(resource)); err != nil {
		t.Fatal(err)
	}
	initErr := errors.New("startup failed")
	lifecycle.Add(&strictFailOnceLifecycleComponent{initErr: initErr})
	if err := lifecycle.Start(context.Background()); !errors.Is(err, initErr) {
		t.Fatalf("Start = %v, want startup failure", err)
	}
	if got := resource.stops.Load(); got != 1 {
		t.Fatalf("retired resource stops after rollback = %d, want 1", got)
	}
	if lifecycle.canRetryStart() {
		t.Fatal("rollback of prestarted resource must not reset lifecycle for retry")
	}
	if err := lifecycle.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := resource.stops.Load(); got != 1 {
		t.Fatalf("resource stops after repeated stop = %d, want 1", got)
	}
}

func TestShutdownClosesRemovedPrestartedRedis(t *testing.T) {
	resetGinModeForTest(t)
	server := miniredis.RunT(t)
	config := NewSysConfig()
	config.SetFrameworkStrict(true)
	config.Redis.Required = true
	config.Redis.Addr = server.Addr()
	app, err := IgniteE(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := app.EnableRedisE(context.Background()); err != nil {
		t.Fatal(err)
	}
	adapter, err := ResolveE[*RedisAdapter](app.Runtime().Container)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = adapter.Client.Close() })
	if err := app.Runtime().Container.TryRemove(reflect.TypeOf(adapter)); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := app.Shutdown(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if err := adapter.Client.Ping(context.Background()).Err(); err == nil || !strings.Contains(err.Error(), "closed") {
		t.Fatalf("removed Redis ping after Shutdown = %v, want closed client", err)
	}
}
