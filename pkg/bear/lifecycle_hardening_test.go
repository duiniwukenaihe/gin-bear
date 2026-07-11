package bear

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type gatedFailingInitializer struct {
	name    string
	err     error
	entered chan struct{}
	release chan struct{}
	calls   atomic.Int32
}

func (c *gatedFailingInitializer) Name() string { return c.name }

func (c *gatedFailingInitializer) Init(context.Context) error {
	if c.calls.Add(1) == 1 {
		close(c.entered)
	}
	<-c.release
	return c.err
}

type blockingContextShutdowner struct {
	name    string
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (c *blockingContextShutdowner) Name() string { return c.name }

func (c *blockingContextShutdowner) ShutdownContext(context.Context) error {
	c.once.Do(func() { close(c.started) })
	<-c.release
	return nil
}

func TestProductionRejectsPlaceholderJWTSecrets(t *testing.T) {
	t.Setenv("BEAR_ENV", "prod")
	for _, secret := range []string{
		"replace-with-at-least-32-random-characters",
		"replace-with-at-least-64-random-characters",
		"replace_with_at_least_32_random_characters",
		"CHANGE-ME-to-a-random-production-secret",
	} {
		t.Run(secret, func(t *testing.T) {
			config := NewSysConfig()
			config.Auth.JWTSecret = secret
			if err := validateProductionSecurity(config); err == nil || !strings.Contains(err.Error(), "weak jwt secret") {
				t.Fatalf("validateProductionSecurity() error = %v, want placeholder rejection", err)
			}
		})
	}
}

func TestProductionRejectsUnsafeConfiguredTimeouts(t *testing.T) {
	t.Setenv("BEAR_ENV", "prod")
	tests := []struct {
		name string
		set  func(*SysConfig)
	}{
		{name: "read header zero", set: func(c *SysConfig) { c.Server.ReadHeaderTimeout = "0s" }},
		{name: "read negative", set: func(c *SysConfig) { c.Server.ReadTimeout = "-1s" }},
		{name: "write excessive", set: func(c *SysConfig) { c.Server.WriteTimeout = "24h" }},
		{name: "idle zero", set: func(c *SysConfig) { c.Server.IdleTimeout = "0s" }},
		{name: "shutdown excessive", set: func(c *SysConfig) { c.Server.ShutdownTimeout = "2h" }},
		{name: "readiness zero", set: func(c *SysConfig) { c.Health.ReadinessTimeout = "0s" }},
		{name: "slow request negative", set: func(c *SysConfig) { c.Middleware.SlowRequestThreshold = "-1ms" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := NewSysConfig()
			config.Auth.JWTSecret = "a-real-production-secret-with-more-than-32-characters"
			tt.set(config)
			if err := validateProductionSecurity(config); err == nil || !strings.Contains(err.Error(), "timeout") {
				t.Fatalf("validateProductionSecurity() error = %v, want timeout rejection", err)
			}
		})
	}
}

func TestApplyAllCachesFailureAndInitializesOnceConcurrently(t *testing.T) {
	initErr := errors.New("deterministic init failure")
	component := &gatedFailingInitializer{
		name:    "failing",
		err:     initErr,
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	app := Ignite(NewSysConfig()).Beans(component)

	const callers = 12
	results := make(chan error, callers)
	for range callers {
		go func() { results <- app.ApplyAll(context.Background()) }()
	}
	<-component.entered
	close(component.release)

	for range callers {
		if err := <-results; !errors.Is(err, initErr) {
			t.Fatalf("ApplyAll() error = %v, want cached initializer error", err)
		}
	}
	if got := component.calls.Load(); got != 1 {
		t.Fatalf("initializer calls = %d, want 1", got)
	}
	if err := app.ApplyAll(context.Background()); !errors.Is(err, initErr) {
		t.Fatalf("repeated ApplyAll() error = %v, want cached initializer error", err)
	}
}

func TestApplyAllRollsBackStartedComponentsInLIFOOrder(t *testing.T) {
	var events []string
	failing := &failingInitComponent{events: &events}
	app := Ignite(NewSysConfig())
	app.Runtime().Lifecycle.Add(recordingComponent{name: "first", events: &events})
	app.Runtime().Lifecycle.Add(recordingComponent{name: "second", events: &events})
	app.Beans(failing)

	if err := app.ApplyAll(context.Background()); err == nil {
		t.Fatal("ApplyAll() error = nil, want initializer failure")
	}
	assertStrings(t, events, []string{
		"start:first",
		"start:second",
		"start:failing-init",
		"stop:second",
		"stop:first",
	})
}

func TestLaunchRejectsCachedApplyFailure(t *testing.T) {
	initErr := errors.New("launch must stay closed")
	component := &gatedFailingInitializer{
		name:    "failing",
		err:     initErr,
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	close(component.release)
	app := Ignite(NewSysConfig()).Beans(component)
	if err := app.ApplyAll(context.Background()); !errors.Is(err, initErr) {
		t.Fatalf("ApplyAll() error = %v, want initializer error", err)
	}
	if err := app.Launch(context.Background()); !errors.Is(err, initErr) {
		t.Fatalf("Launch() error = %v, want cached initializer error", err)
	}
}

func TestShutdownContextHooksAreDeadlineBoundAndInstanceScoped(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	components := []*blockingContextShutdowner{
		{name: "first", started: make(chan struct{}), release: release},
		{name: "second", started: make(chan struct{}), release: release},
	}
	lifecycles := []*Lifecycle{newLifecycle(), newLifecycle()}
	for i := range lifecycles {
		lifecycles[i].Add(components[i])
		if err := lifecycles[i].Start(context.Background()); err != nil {
			t.Fatal(err)
		}
	}

	errs := make(chan error, len(lifecycles))
	for _, lifecycle := range lifecycles {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
			defer cancel()
			errs <- lifecycle.Stop(ctx)
		}()
	}
	for _, component := range components {
		select {
		case <-component.started:
		case <-time.After(200 * time.Millisecond):
			t.Fatal("one application shutdown blocked the other application hook")
		}
	}
	for range lifecycles {
		if err := <-errs; !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Stop() error = %v, want deadline exceeded", err)
		}
	}
}
